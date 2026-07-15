package dto

import "time"

// StatisticsSnapshot is the repository-layer usage statistics snapshot.
type StatisticsSnapshot struct {
	TotalRequests    int64                                               `json:"total_requests"`
	SuccessCount     int64                                               `json:"success_count"`
	FailureCount     int64                                               `json:"failure_count"`
	TotalTokens      int64                                               `json:"total_tokens"`
	APIs             map[string]APISnapshot                              `json:"apis"`
	RequestsByDay    map[string]int64                                    `json:"requests_by_day"`
	RequestsByHour   map[string]int64                                    `json:"requests_by_hour"`
	TokensByDay      map[string]int64                                    `json:"tokens_by_day"`
	TokensByHour     map[string]int64                                    `json:"tokens_by_hour"`
	CredentialHourly map[string]map[string]UsageCredentialBucketSnapshot `json:"-"`
}

// APISnapshot is one API group in a usage statistics snapshot.
type APISnapshot struct {
	DisplayName   string                   `json:"display_name,omitempty"`
	TotalRequests int64                    `json:"total_requests"`
	SuccessCount  int64                    `json:"success_count"`
	FailureCount  int64                    `json:"failure_count"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// UsageBucketSnapshot stores compact metrics for one hourly bucket.
type UsageBucketSnapshot struct {
	TotalRequests      int64
	SuccessCount       int64
	FailureCount       int64
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CachedTokens       int64
	TotalTokens        int64
	TotalLatencyMS     int64
	LatencySampleCount int64
}

type UsageCredentialBucketSnapshot struct {
	Source          string
	AuthIndex       string
	Model           string
	SuccessCount    int64
	FailureCount    int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
}

// ModelSnapshot is one model in a usage statistics snapshot.
type ModelSnapshot struct {
	TotalRequests      int64                          `json:"total_requests"`
	SuccessCount       int64                          `json:"success_count"`
	FailureCount       int64                          `json:"failure_count"`
	TotalTokens        int64                          `json:"total_tokens"`
	InputTokens        int64                          `json:"input_tokens,omitempty"`
	OutputTokens       int64                          `json:"output_tokens,omitempty"`
	ReasoningTokens    int64                          `json:"reasoning_tokens,omitempty"`
	CachedTokens       int64                          `json:"cached_tokens,omitempty"`
	TotalLatencyMS     int64                          `json:"total_latency_ms"`
	LatencySampleCount int64                          `json:"latency_sample_count"`
	Hourly             map[string]UsageBucketSnapshot `json:"-"`
	Details            []RequestDetail                `json:"details"`
}

// RequestDetail is one usage request detail.
type RequestDetail struct {
	Timestamp     time.Time  `json:"timestamp"`
	LatencyMS     int64      `json:"latency_ms"`
	Source        string     `json:"source"`
	SourceRaw     string     `json:"source_raw,omitempty"`
	SourceDisplay string     `json:"source_display,omitempty"`
	SourceType    string     `json:"source_type,omitempty"`
	AuthIndex     string     `json:"auth_index"`
	Failed        bool       `json:"failed"`
	Tokens        TokenStats `json:"tokens"`
}

// TokenStats stores token counts for one request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}
