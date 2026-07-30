package dto

import "time"

// UsageEventsPageRecord is the repository query result for a usage-events list page.
type UsageEventsPageRecord struct {
	Events     []UsageEventRecord
	Models     []string
	TotalCount int64
	Page       int
	PageSize   int
	TotalPages int
}

// UsageEventFilterOptionsRecord is the repository query result for usage-events filter options.
type UsageEventFilterOptionsRecord struct {
	Models []string
}

// UsageEventRecord is the query result for a single usage event.
type UsageEventRecord struct {
	ID              uint
	Timestamp       time.Time
	APIGroupKey     string
	Model           string
	AuthType        string
	Provider        string
	Source          string
	AuthIndex       string
	Failed          bool
	LatencyMS       int64
	FirstTokenMS    int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
}
