package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type oplogtoredisConfiguration struct {
	RedisURL               string        `required:"true" split_words:"true"`
	MongoURL               string        `required:"true" split_words:"true"`
	BufferSize             int           `default:"10000" split_words:"true"`
	TimestampFlushInterval time.Duration `default:"1s" split_words:"true"`
	MaxCatchUp             time.Duration `default:"60s" split_words:"true"`
	RedisDedupeExpiration  time.Duration `default:"120s" split_words:"true"`
	RedisMetadataPrefix    string        `default:"oplogtoredis::" split_words:"true"`
}

var globalConfig *oplogtoredisConfiguration

// RedisURL is a read-only accessor for the Redis URL configuration
func RedisURL() string {
	return globalConfig.RedisURL
}

// MongoURL is a read-only accessor for the Mongo URL configuration
func MongoURL() string {
	return globalConfig.MongoURL
}

// BufferSize is the size of the internal buffers that hold oplog messages while
// they're being processed.
func BufferSize() int {
	return globalConfig.BufferSize
}

// TimestampFlushInterval is how frequently to flush the timestamp of the last
// processed message to Redis. When we start up, we start tailing the oplog from
// where we left off (as indicated by this timestamp).
func TimestampFlushInterval() time.Duration {
	return globalConfig.TimestampFlushInterval
}

// MaxCatchUp is the maximum length of time for which we process old oplog
// entries. When starting up, if the timestamp of the last entry processes is
// more than MaxCatchUp ago, we don't try to catch up and just start processing
// the oplog from the end. If it's less than MaxCatchUp, we process oplog
// entries starting from the timestamp. This allows us to catch up if
// oplogtoredis exists and then starts back up.
func MaxCatchUp() time.Duration {
	return globalConfig.MaxCatchUp
}

// RedisDedupeExpiration controls the expiration of the Redis keys that are
// used to ensure we process oplog entries at most once. Every time we publish
// an oplog entry to Redis, we write its unique timestamp as a Redis expiring
// key, and check for the existence of that key before doing the actual publish.
// This allows us to both run multiple copies of oplogtoredis (only one will
// get to write the key and send the message, the other one will see the key
// exists and skip publishing), and also ensures that on restart we don't
// re-publish entries from in between the last time the
// latest-processed-timestamp was updated in Redis and whne the process
// existed.
func RedisDedupeExpiration() time.Duration {
	return globalConfig.RedisDedupeExpiration
}

// RedisMetadataPrefix controls the prefix for keys used to store oplogtoredis
// metadata (such as the timestamp of the last oplog entry processed). If you're
// running multiple instances of oplogtoredis for the same MongoDB (for high
// availability), you should use the same RedisMetadataPrefix for both. If
// you're running multiple instances for different MongoDBs (because you're
// using many MongoDB instances with a shared Redis instace, for example), you
// should have different RedisMetadataPrefixes for each.
//
// This *does not* affect the channel names used to publish oplog entries.
// The channel names are always <db-name>.<collection-name> and
// <db-name>.<collection-name>::<document-id>
func RedisMetadataPrefix() string {
	return globalConfig.RedisMetadataPrefix
}

// ParseEnv parses the current environment variables and updates the stored
// configuration. It is *not* threadsafe, and should just be called once
// at the start of the program.
func ParseEnv() error {
	var config oplogtoredisConfiguration

	err := envconfig.Process("otr", &config)
	if err != nil {
		return err
	}

	globalConfig = &config
	return nil
}
