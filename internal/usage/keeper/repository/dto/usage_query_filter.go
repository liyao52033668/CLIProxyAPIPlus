package dto

import "time"

// UsageQueryFilter 是仓储层的 usage 查询条件。
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
