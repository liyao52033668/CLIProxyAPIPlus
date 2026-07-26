package dto

import "time"

// UsageQueryFilter is the repository-layer usage query filter.
type UsageQueryFilter struct {
	Range     string
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
	Page      int
	PageSize  int
	Offset    int
	Model     string
	Source    string
	AuthIndex string
	Result    string
}

const DefaultUsageEventsLimit = 100
