package dto

import "time"

// RedisInboxInsert 是 Redis usage inbox 的入库参数。
type RedisInboxInsert struct {
	QueueKey   string
	RawMessage string
	PoppedAt   time.Time
}

// RedisUsageInboxCleanupResult 是 Redis usage inbox 的清理结果。
type RedisUsageInboxCleanupResult struct {
	ProcessedDeleted int64
	FailedDeleted    int64
}
