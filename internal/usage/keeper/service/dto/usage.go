package dto

import (
	"time"

	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
)

const DefaultUsageEventsLimit = 100

// UsageFilter defines service-layer usage query conditions.
type UsageFilter struct {
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

// UsageImportResult reports the result of importing a usage snapshot.
type UsageImportResult struct {
	Added         int   `json:"added"`
	Skipped       int   `json:"skipped"`
	TotalRequests int64 `json:"total_requests"`
	FailedCount   int64 `json:"failed_requests"`
}

// UsageEventsPage is the service-layer usage event list result.
type UsageEventsPage struct {
	Events     []UsageEventRecord `json:"events"`
	Models     []string           `json:"models"`
	TotalCount int64              `json:"total_count"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	TotalPages int                `json:"total_pages"`
}

// UsageEventFilterOptions is the service-layer event filter result.
type UsageEventFilterOptions struct {
	Models []string `json:"models"`
}

// UsageEventRecord is one service-layer usage event.
type UsageEventRecord struct {
	ID              uint      `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	APIGroupKey     string    `json:"api_group_key"`
	Model           string    `json:"model"`
	AuthType        string    `json:"auth_type"`
	Provider        string    `json:"provider"`
	Source          string    `json:"source"`
	AuthIndex       string    `json:"auth_index"`
	Failed          bool      `json:"failed"`
	LatencyMS       int64     `json:"latency_ms"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	CachedTokens    int64     `json:"cached_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
}

// UsageAnalysisModelStat is the per-model analysis aggregate.
type UsageAnalysisModelStat struct {
	Model              string
	TotalRequests      int64
	SuccessCount       int64
	FailureCount       int64
	TotalTokens        int64
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CachedTokens       int64
	TotalLatencyMS     int64
	LatencySampleCount int64
}

// UsageAnalysisAPIStat is the per-API analysis aggregate.
type UsageAnalysisAPIStat struct {
	APIKey          string
	DisplayName     string
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	Models          []UsageAnalysisModelStat
}

// UsageAnalysisSnapshot is the service-layer analysis result.
type UsageAnalysisSnapshot struct {
	APIs   []UsageAnalysisAPIStat
	Models []UsageAnalysisModelStat
}

// UsageOverviewSummary is the service-layer overview summary.
type UsageOverviewSummary struct {
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

// UsageOverviewSeries is the service-layer overview series.
type UsageOverviewSeries struct {
	Requests        map[string]int64
	Tokens          map[string]int64
	RPM             map[string]float64
	TPM             map[string]float64
	Cost            map[string]float64
	InputTokens     map[string]int64
	OutputTokens    map[string]int64
	CachedTokens    map[string]int64
	ReasoningTokens map[string]int64
	Models          map[string]UsageOverviewSeries
}

// UsageOverviewHealthBlock is one service-layer health time block.
type UsageOverviewHealthBlock struct {
	StartTime time.Time
	EndTime   time.Time
	Success   int64
	Failure   int64
	Rate      float64
}

// UsageOverviewHealth is the service-layer health aggregate.
type UsageOverviewHealth struct {
	TotalSuccess  int64
	TotalFailure  int64
	SuccessRate   float64
	Rows          int
	Columns       int
	BucketSeconds int64
	WindowStart   time.Time
	WindowEnd     time.Time
	BlockDetails  []UsageOverviewHealthBlock
}

// UsageKeyCount is a compact request-outcome counter for one key.
type UsageKeyCount struct {
	Success int64   `json:"success"`
	Failure int64   `json:"failure"`
	Tokens  int64   `json:"tokens"`
	Cost    float64 `json:"cost"`
}

// UsageCredentialCount keeps the source associated with its auth index.
type UsageCredentialCount struct {
	Source    string  `json:"source"`
	AuthIndex string  `json:"auth_index"`
	Success   int64   `json:"success"`
	Failure   int64   `json:"failure"`
	Tokens    int64   `json:"tokens"`
	Cost      float64 `json:"cost"`
}

// UsageKeyStats aggregates request outcomes by auth_index and source for the
// management UI, without shipping per-request details.
type UsageKeyStats struct {
	BySource    map[string]UsageKeyCount `json:"by_source"`
	ByAuthIndex map[string]UsageKeyCount `json:"by_auth_index"`
	Credentials []UsageCredentialCount   `json:"credentials"`
}

// UsageOverviewSnapshot is the service-layer overview result.
type UsageOverviewSnapshot struct {
	Usage        *repodto.StatisticsSnapshot
	Summary      UsageOverviewSummary
	Series       UsageOverviewSeries
	HourlySeries UsageOverviewSeries
	DailySeries  UsageOverviewSeries
	Health       UsageOverviewHealth
	KeyStats     UsageKeyStats `json:"key_stats"`
	StartTime    *time.Time
	EndTime      *time.Time
	BucketByDay  bool
}
