package entities

import "time"

// UsageHourlyAggregate stores compact hourly usage statistics after request details expire.
type UsageHourlyAggregate struct {
	AggregateKey       string    `gorm:"primaryKey;size:64"`
	BucketStart        time.Time `gorm:"index:idx_usage_hourly_aggregates_bucket_start"`
	APIGroupKey        string
	Provider           string
	AuthType           string
	Model              string
	Source             string
	AuthIndex          string
	RequestCount       int64
	SuccessCount       int64
	FailureCount       int64
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CachedTokens       int64
	TotalTokens        int64
	TotalLatencyMS     int64
	LatencySampleCount int64
	FirstEventAt       time.Time
	LastEventAt        time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// UsageEventKey keeps a compact deduplication marker after the request detail is removed.
type UsageEventKey struct {
	EventKeyHash   string    `gorm:"primaryKey;size:64"`
	EventTimestamp time.Time `gorm:"index:idx_usage_event_keys_timestamp"`
	CreatedAt      time.Time
}
