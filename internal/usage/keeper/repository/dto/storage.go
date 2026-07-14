package dto

import "time"

// UsageEventCleanupResult reports request-detail archival work.
type UsageEventCleanupResult struct {
	Cutoff            time.Time
	ArchivedEvents    int64
	UpdatedAggregates int64
	DeletedEventKeys  int64
}

// StorageCleanupResult reports the daily repository maintenance result.
type StorageCleanupResult struct {
	RedisInbox  RedisUsageInboxCleanupResult
	UsageEvents UsageEventCleanupResult
	Vacuumed    bool
}
