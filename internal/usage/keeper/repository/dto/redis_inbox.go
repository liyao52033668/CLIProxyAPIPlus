package dto

import "time"

// RedisInboxInsert is the insert payload for the Redis usage inbox.
type RedisInboxInsert struct {
	QueueKey   string
	RawMessage string
	PoppedAt   time.Time
}

// RedisUsageInboxCleanupResult is the cleanup result for the Redis usage inbox.
type RedisUsageInboxCleanupResult struct {
	ProcessedDeleted int64
	FailedDeleted    int64
}
