package usage

// CacheDetails holds prompt cache configuration and TTL-bucketed write token counts.
type CacheDetails struct {
	Mode                    string           `json:"mode,omitempty"`
	TTL                     string           `json:"ttl,omitempty"`
	Retention               string           `json:"retention,omitempty"`
	Ephemeral5mInputTokens  *int64           `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral30mInputTokens *int64           `json:"ephemeral_30m_input_tokens,omitempty"`
	Ephemeral1hInputTokens  *int64           `json:"ephemeral_1h_input_tokens,omitempty"`
	Ephemeral12hInputTokens *int64           `json:"ephemeral_12h_input_tokens,omitempty"`
	ByTTL                   map[string]int64 `json:"by_ttl,omitempty"`
}

// ToolUsageDetails holds structured server-side tool execution counters.
type ToolUsageDetails struct {
	WebSearchCalls       *int64           `json:"web_search_calls,omitempty"`
	WebSearchQueries     *int64           `json:"web_search_queries,omitempty"`
	WebFetchCalls        *int64           `json:"web_fetch_calls,omitempty"`
	NumSearchResults     *int64           `json:"num_search_results,omitempty"`
	NumSourcesUsed       *int64           `json:"num_sources_used,omitempty"`
	ToolCallsRequested   *int64           `json:"tool_calls_requested,omitempty"`
	ToolCallsExecuted    *int64           `json:"tool_calls_executed,omitempty"`
	XSearchCalls         *int64           `json:"x_search_calls,omitempty"`
	XPostsFetched        *int64           `json:"x_posts_fetched,omitempty"`
	XUsersFetched        *int64           `json:"x_users_fetched,omitempty"`
	CodeExecutionCalls   *int64           `json:"code_execution_calls,omitempty"`
	FileSearchCalls      *int64           `json:"file_search_calls,omitempty"`
	MCPCalls             *int64           `json:"mcp_calls,omitempty"`
	DocumentSearchCalls  *int64           `json:"document_search_calls,omitempty"`
	ImageGenerationCalls *int64           `json:"image_generation_calls,omitempty"`
	Extra                map[string]int64 `json:"extra,omitempty"`
}

// ImageDetails holds image/video generation dimension and quality attributes.
type ImageDetails struct {
	Size           string `json:"size,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	ImageSize      string `json:"image_size,omitempty"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Background     string `json:"background,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	InputFidelity  string `json:"input_fidelity,omitempty"`
}

// Clone returns a deep copy of ImageDetails.
func (d *ImageDetails) Clone() *ImageDetails {
	if d == nil {
		return nil
	}
	out := *d
	return &out
}

// MediaCounts holds prompt/completion media item counts and audio/video duration.
type MediaCounts struct {
	PromptMedia     *int64   `json:"prompt_media,omitempty"`
	CompletionMedia *int64   `json:"completion_media,omitempty"`
	PromptAudio     *int64   `json:"prompt_audio,omitempty"`
	AudioSeconds    *float64 `json:"audio_seconds,omitempty"`
}

// Clone returns a deep copy of MediaCounts.
func (m *MediaCounts) Clone() *MediaCounts {
	if m == nil {
		return nil
	}
	return &MediaCounts{
		PromptMedia:     cloneInt64Ptr(m.PromptMedia),
		CompletionMedia: cloneInt64Ptr(m.CompletionMedia),
		PromptAudio:     cloneInt64Ptr(m.PromptAudio),
		AudioSeconds:    cloneFloat64Ptr(m.AudioSeconds),
	}
}

// CostDetails holds optional upstream-reported cost information (e.g. OpenRouter).
type CostDetails struct {
	Cost                             *float64 `json:"cost,omitempty"`
	CostInUSDTicks                   *int64   `json:"cost_in_usd_ticks,omitempty"`
	IsBYOK                           *bool    `json:"is_byok,omitempty"`
	UpstreamInferenceCost            *float64 `json:"upstream_inference_cost,omitempty"`
	UpstreamInferencePromptCost      *float64 `json:"upstream_inference_prompt_cost,omitempty"`
	UpstreamInferenceCompletionsCost *float64 `json:"upstream_inference_completions_cost,omitempty"`
}

// Int64Ptr returns a pointer to v.
func Int64Ptr(v int64) *int64 {
	return &v
}

// Float64Ptr returns a pointer to v.
func Float64Ptr(v float64) *float64 {
	return &v
}

// BoolPtr returns a pointer to v.
func BoolPtr(v bool) *bool {
	return &v
}

// CloneInt64Ptr returns a deep copy of p.
func CloneInt64Ptr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	out := *p
	return &out
}

func cloneInt64Ptr(p *int64) *int64 {
	return CloneInt64Ptr(p)
}

func cloneFloat64Ptr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	out := *p
	return &out
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	out := *p
	return &out
}

// CloneToolUsageDetails returns a deep copy of t.
func CloneToolUsageDetails(t *ToolUsageDetails) *ToolUsageDetails {
	if t == nil {
		return nil
	}
	out := &ToolUsageDetails{
		WebSearchCalls:       cloneInt64Ptr(t.WebSearchCalls),
		WebSearchQueries:     cloneInt64Ptr(t.WebSearchQueries),
		WebFetchCalls:        cloneInt64Ptr(t.WebFetchCalls),
		NumSearchResults:     cloneInt64Ptr(t.NumSearchResults),
		NumSourcesUsed:       cloneInt64Ptr(t.NumSourcesUsed),
		ToolCallsRequested:   cloneInt64Ptr(t.ToolCallsRequested),
		ToolCallsExecuted:    cloneInt64Ptr(t.ToolCallsExecuted),
		XSearchCalls:         cloneInt64Ptr(t.XSearchCalls),
		XPostsFetched:        cloneInt64Ptr(t.XPostsFetched),
		XUsersFetched:        cloneInt64Ptr(t.XUsersFetched),
		CodeExecutionCalls:   cloneInt64Ptr(t.CodeExecutionCalls),
		FileSearchCalls:      cloneInt64Ptr(t.FileSearchCalls),
		MCPCalls:             cloneInt64Ptr(t.MCPCalls),
		DocumentSearchCalls:  cloneInt64Ptr(t.DocumentSearchCalls),
		ImageGenerationCalls: cloneInt64Ptr(t.ImageGenerationCalls),
	}
	if t.Extra != nil {
		out.Extra = make(map[string]int64, len(t.Extra))
		for key, value := range t.Extra {
			out.Extra[key] = value
		}
	}
	return out
}
