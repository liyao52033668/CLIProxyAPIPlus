package dto

// UsageAnalysisModelStatRecord 是按模型聚合的分析结果。
type UsageAnalysisModelStatRecord struct {
	Model              string
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

// UsageAnalysisAPIStatRecord 是按 API 聚合的分析结果。
type UsageAnalysisAPIStatRecord struct {
	APIGroupKey     string
	DisplayName     string
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
	Models          []UsageAnalysisModelStatRecord `gorm:"-"`
}
