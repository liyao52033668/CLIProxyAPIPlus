package dto

import "time"

// UsageOverviewSummaryRecord is the repository-layer overview summary.
type UsageOverviewSummaryRecord struct {
	RequestCount    int64
	TokenCount      int64
	WindowMinutes   int64
	RPM             float64
	TPM             float64
	TotalCost       float64
	CostAvailable   bool
	CachedTokens    int64
	ReasoningTokens int64
}

// UsageOverviewSeriesRecord is the repository-layer overview series.
type UsageOverviewSeriesRecord struct {
	Requests        map[string]int64
	Tokens          map[string]int64
	RPM             map[string]float64
	TPM             map[string]float64
	Cost            map[string]float64
	InputTokens     map[string]int64
	OutputTokens    map[string]int64
	CachedTokens    map[string]int64
	ReasoningTokens map[string]int64
	Models          map[string]UsageOverviewSeriesRecord
}

// UsageOverviewHealthBlockRecord is one repository-layer health time block.
type UsageOverviewHealthBlockRecord struct {
	StartTime time.Time
	EndTime   time.Time
	Success   int64
	Failure   int64
	Rate      float64
}

// UsageOverviewHealthRecord is the repository-layer health aggregate.
type UsageOverviewHealthRecord struct {
	TotalSuccess  int64
	TotalFailure  int64
	SuccessRate   float64
	Rows          int
	Columns       int
	BucketSeconds int64
	WindowStart   time.Time
	WindowEnd     time.Time
	BlockDetails  []UsageOverviewHealthBlockRecord
}

// UsageKeyCountRecord is a compact request-outcome counter for one key.
type UsageKeyCountRecord struct {
	Success int64
	Failure int64
	Tokens  int64
	Cost    float64
}

// UsageCredentialKey identifies one credential without losing its source association.
type UsageCredentialKey struct {
	Source    string
	AuthIndex string
}

// UsageKeyStatsRecord aggregates request outcomes by auth_index and source.
// The management UI uses this instead of replaying per-request details.
type UsageKeyStatsRecord struct {
	BySource    map[string]UsageKeyCountRecord
	ByAuthIndex map[string]UsageKeyCountRecord
	Credentials map[UsageCredentialKey]UsageKeyCountRecord
}

// UsageOverviewRecord is the complete repository-layer usage overview result.
type UsageOverviewRecord struct {
	Usage        *StatisticsSnapshot
	Summary      UsageOverviewSummaryRecord
	Series       UsageOverviewSeriesRecord
	HourlySeries UsageOverviewSeriesRecord
	DailySeries  UsageOverviewSeriesRecord
	Health       UsageOverviewHealthRecord
	KeyStats     UsageKeyStatsRecord
}
