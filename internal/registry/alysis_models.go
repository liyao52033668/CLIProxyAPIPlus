// Package registry provides model definitions for various AI service providers.
package registry

// GetAlysisModels returns the static Alysis Code Pro fallback catalog. It
// mirrors the models the reference CLI observed on the gateway; the live
// catalog is fetched at runtime from the gateway's OpenAI-shaped /models
// endpoint and this list is only used when that fetch fails.
func GetAlysisModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                  "deepseek-v4-flash",
			Object:              "model",
			Created:             1780937400,
			OwnedBy:             "alysis",
			Type:                "alysis",
			DisplayName:         "DeepSeek V4 Flash",
			Description:         "Alysis Code Pro flagship default model",
			ContextLength:       128000,
			MaxCompletionTokens: 128000,
		},
		{
			ID:                  "deepseek-v4-pro",
			DisplayName:         "DeepSeek V4 Pro",
			OwnedBy:             "alysis",
			Type:                "alysis",
			Object:              "model",
			Created:             1780937400,
			ContextLength:       128000,
			MaxCompletionTokens: 128000,
		},
		{
			ID:                  "deepseek-v4-flash-vision-exp",
			DisplayName:         "DeepSeek V4 Flash Vision (Exp)",
			OwnedBy:             "alysis",
			Type:                "alysis",
			Object:              "model",
			Created:             1780937400,
			ContextLength:       128000,
			MaxCompletionTokens: 128000,
		},
		{
			ID:                  "deepseek-v4.1-flash",
			DisplayName:         "DeepSeek V4.1 Flash",
			OwnedBy:             "alysis",
			Type:                "alysis",
			Object:              "model",
			Created:             1789889200,
			ContextLength:       128000,
			MaxCompletionTokens: 128000,
		},
	}
}
