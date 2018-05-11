package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/tulip/oplogtoredis/lib/config"
	"github.com/tulip/oplogtoredis/lib/log"
	"github.com/tulip/oplogtoredis/lib/mongourl"
	"github.com/tulip/oplogtoredis/lib/oplog"
	"github.com/tulip/oplogtoredis/lib/redispub"
	"go.uber.org/zap"

	"github.com/globalsign/mgo"
	"github.com/go-redis/redis"
	"github.com/rwynn/gtm"
)

func main() {
	defer log.RawLog.Sync()

	err := config.ParseEnv()
	if err != nil {
		panic("Error parsing environment variables: " + err.Error())
	}

	mongoSession, gtmSession, err := createGTMClient()
	if err != nil {
		panic("Error initialize oplog tailer: " + err.Error())
	}
	defer mongoSession.Close()
	defer gtmSession.Stop()
	log.Log.Info("Initialized connection to Mongo")

	redisClient, err := createRedisClient()
	if err != nil {
		panic("Error initializing Redis client: " + err.Error())
	}
	defer redisClient.Close()
	log.Log.Info("Initialized connection to Redis")

	// We crate two goroutines:
	//
	// The oplog.Tail goroutine reads messages from the oplog, and generates the
	// messages that we need to write to redis. It then writes them to a
	// buffered channel.
	//
	// The redispub.PublishStream goroutine reads messages from the buffered channel
	// and sends them to Redis.
	//
	// TODO PERF: Use a leaky buffer (https://github.com/tulip/oplogtoredis/issues/2)
	redisPubs := make(chan *redispub.Publication, 10000)

	stopOplogTail := make(chan bool)
	go oplog.Tail(gtmSession.OpC, redisPubs, stopOplogTail)

	stopRedisPub := make(chan bool)
	go redispub.PublishStream(redisClient, redisPubs, &redispub.PublishOpts{
		FlushInterval:    config.TimestampFlushInterval(),
		DedupeExpiration: config.RedisDedupeExpiration(),
		MetadataPrefix:   config.RedisMetadataPrefix(),
	}, stopRedisPub)
	log.Log.Info("Started up processing goroutines")

	// Now we just wait until we get an exit signal, then exit cleanly
	//
	// We must use a buffered channel or risk missing the signal
	// if we're not ready to receive when the signal is sent.
	// See examples from https://golang.org/pkg/os/signal/#Notify
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	sig := <-signalChan

	// We got a SIGINT, cleanly stop background goroutines and then return so
	// that the `defer`s above can close the Mongo and Redis connection.
	//
	// We also call signal.Reset() to clear our signal handler so if we get
	// another SIGINT we immediately exit without cleaning up.
	log.Log.Warnf("Exiting cleanly due to signal %s. Interrupt again to force unclean shutdown.", sig)
	signal.Reset()

	stopOplogTail <- true
	stopRedisPub <- true
}

// Connects to mongo, starts up a gtm client, and starts up a background
// goroutine to log GTM errors
func createGTMClient() (*mgo.Session, *gtm.OpCtx, error) {
	// configure mgo to use our logger
	stdLog, err := zap.NewStdLogAt(log.RawLog, zap.InfoLevel)
	if err != nil {
		return nil, nil, fmt.Errorf("Could not create a std logger: %s", err)
	}

	mgo.SetLogger(stdLog)

	// get a mgo session
	dialInfo, err := mongourl.Parse(config.MongoURL())
	if err != nil {
		return nil, nil, fmt.Errorf("Could not parse Mongo URL: %s", err)
	}

	session, err := mgo.DialWithInfo(dialInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("Error connecting to Mongo: %s", err)
	}

	session.SetMode(mgo.Monotonic, true)

	// Use gtm to tail to oplog
	//
	// TODO PERF: benchmark other oplog tailers (https://github.com/tulip/oplogtoredis/issues/3)
	//
	// TODO: pick up where we left off on restart (https://github.com/tulip/oplogtoredis/issues/4)
	ctx := gtm.Start(session, &gtm.Options{
		ChannelSize:       10000,
		BufferDuration:    100 * time.Millisecond,
		UpdateDataAsDelta: true,
		WorkerCount:       8,
	})

	// Start a goroutine to log gtm errors
	go func() {
		for {
			err := <-ctx.ErrC

			log.Log.Errorw("Error tailing oplog",
				"error", err)
		}
	}()

	return session, ctx, nil
}

// Goroutine that just reads messages and sends them to Redis. We don't do this
// inline above so that messages can queue up in the channel if we lose our
// redis connection
func createRedisClient() (redis.UniversalClient, error) {
	// Configure go-redis to use our logger
	stdLog, err := zap.NewStdLogAt(log.RawLog, zap.InfoLevel)
	if err != nil {
		return nil, fmt.Errorf("Could not create a std logger: %s", err)
	}

	redis.SetLogger(stdLog)

	// Parse the Redis URL
	parsedRedisURL, err := redis.ParseURL(config.RedisURL())
	if err != nil {
		return nil, fmt.Errorf("Error parsing Redis URL: %s", err)
	}

	// Create a Redis client
	client := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:    []string{parsedRedisURL.Addr},
		DB:       parsedRedisURL.DB,
		Password: parsedRedisURL.Password,
	})

	// Check that we have a connection
	_, err = client.Ping().Result()
	if err != nil {
		return nil, fmt.Errorf("Redis ping failed: %s", err)
	}

	return client, nil
}
