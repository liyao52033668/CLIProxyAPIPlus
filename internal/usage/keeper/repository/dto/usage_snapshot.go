package dto

import "time"

// StatisticsSnapshot 是仓储层构建的 usage 统计快照。
type StatisticsSnapshot struct {
	TotalRequests  int64                  `json:"total_requests"`
	SuccessCount   int64                  `json:"success_count"`
	FailureCount   int64                  `json:"failure_count"`
	TotalTokens    int64                  `json:"total_tokens"`
	APIs           map[string]APISnapshot `json:"apis"`
	RequestsByDay  map[string]int64       `json:"requests_by_day"`
	RequestsByHour map[string]int64       `json:"requests_by_hour"`
	TokensByDay    map[string]int64       `json:"tokens_by_day"`
	TokensByHour   map[string]int64       `json:"tokens_by_hour"`
}

// APISnapshot 是按 API 分组的 usage 统计快照。
type APISnapshot struct {
	DisplayName   string                   `json:"display_name,omitempty"`
	TotalRequests int64                    `json:"total_requests"`
	SuccessCount  int64                    `json:"success_count"`
	FailureCount  int64                    `json:"failure_count"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot 是按模型分组的 usage 统计快照。
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	SuccessCount  int64           `json:"success_count"`
	FailureCount  int64           `json:"failure_count"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

// RequestDetail 是单次 usage 请求明细。
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

// TokenStats 是单次请求的 token 统计。
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}
