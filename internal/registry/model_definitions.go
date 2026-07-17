// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is loaded from the embedded models.json file and can be refreshed from network.
package registry

import (
	"strings"
)

const (
	codexBuiltinImageModelID      = "gpt-image-2"
	xaiBuiltinImageModelID        = "grok-imagine-image"
	xaiBuiltinImageQualityModelID = "grok-imagine-image-quality"
	xaiBuiltinVideoModelID        = "grok-imagine-video"
)

// staticModelsJSON mirrors the top-level structure of models.json.
type staticModelsJSON struct {
	Claude      []*ModelInfo `json:"claude"`
	Gemini      []*ModelInfo `json:"gemini"`
	Vertex      []*ModelInfo `json:"vertex"`
	GeminiCLI   []*ModelInfo `json:"gemini-cli"`
	AIStudio    []*ModelInfo `json:"aistudio"`
	CodexFree   []*ModelInfo `json:"codex-free"`
	CodexTeam   []*ModelInfo `json:"codex-team"`
	CodexPlus   []*ModelInfo `json:"codex-plus"`
	CodexPro    []*ModelInfo `json:"codex-pro"`
	Kimi        []*ModelInfo `json:"kimi"`
	Antigravity []*ModelInfo `json:"antigravity"`
	XAI         []*ModelInfo `json:"xai"`
}

// GetClaudeModels returns the standard Claude model definitions.
func GetClaudeModels() []*ModelInfo {
	return cloneModelInfos(getModels().Claude)
}

// GetGeminiModels returns the standard Gemini model definitions.
func GetGeminiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Gemini)
}

// GetGeminiVertexModels returns Gemini model definitions for Vertex AI.
func GetGeminiVertexModels() []*ModelInfo {
	return cloneModelInfos(getModels().Vertex)
}

// GetGeminiCLIModels returns Gemini model definitions for the Gemini CLI.
func GetGeminiCLIModels() []*ModelInfo {
	return cloneModelInfos(getModels().GeminiCLI)
}

// GetAIStudioModels returns model definitions for AI Studio.
func GetAIStudioModels() []*ModelInfo {
	return cloneModelInfos(getModels().AIStudio)
}

// GetCodexFreeModels returns model definitions for the Codex free plan tier.
func GetCodexFreeModels() []*ModelInfo {
	models := cloneModelInfos(getModels().CodexFree)
	filtered := models[:0]
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), "gpt-5.5") {
			continue
		}
		filtered = append(filtered, model)
	}
	return WithCodexBuiltins(filtered)
}

// GetCodexTeamModels returns model definitions for the Codex team plan tier.
func GetCodexTeamModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexTeam))
}

// GetCodexPlusModels returns model definitions for the Codex plus plan tier.
func GetCodexPlusModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPlus))
}

// GetCodexProModels returns model definitions for the Codex pro plan tier.
func GetCodexProModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPro))
}

// GetKimiModels returns the standard Kimi (Moonshot AI) model definitions.
func GetKimiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Kimi)
}

// GetAntigravityModels returns the standard Antigravity model definitions.
func GetAntigravityModels() []*ModelInfo {
	return cloneModelInfos(getModels().Antigravity)
}

// AntigravityWebSearchModelFor returns the Antigravity model that should run a
// native web search request for modelID.
func AntigravityWebSearchModelFor(modelID string) string {
	modelID = normalizeAntigravityCapabilityModelID(modelID)
	if modelID == "" {
		return ""
	}
	for _, model := range GetGlobalRegistry().GetAvailableModelsByProvider("antigravity") {
		if model == nil {
			continue
		}
		currentModelID := normalizeAntigravityCapabilityModelID(model.ID)
		if currentModelID == "" {
			continue
		}
		if currentModelID == modelID {
			if model.SupportsWebSearch {
				return currentModelID
			}
			return ""
		}
	}
	return ""
}

func normalizeAntigravityCapabilityModelID(modelID string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if open := strings.LastIndex(modelID, "("); open >= 0 && strings.HasSuffix(modelID, ")") {
		modelID = strings.TrimSpace(modelID[:open])
	}
	return modelID
}

// GetCodeArtsModels returns the standard CodeArts model definitions.
// CodeArts is Huawei Cloud's AI development platform providing code assistance.
func GetCodeArtsModels() []*ModelInfo {
	now := int64(1748044800) // 2025-05-24
	return []*ModelInfo{
		{
			ID:                  "glm-5-internal",
			Object:              "model",
			Created:             now,
			OwnedBy:             "huaweicloud",
			Type:                "codearts",
			DisplayName:         "GLM-5 Internal",
			Description:         "GLM-5 Internal via CodeArts",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "codearts-glm-5.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "huaweicloud",
			Type:                "codearts",
			DisplayName:         "GLM-5.1",
			Description:         "GLM-5.1 via CodeArts",
			ContextLength:       200000,
			MaxCompletionTokens: 48000,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "deepseek-v3.2",
			Object:              "model",
			Created:             now,
			OwnedBy:             "huaweicloud",
			Type:                "codearts",
			DisplayName:         "DeepSeek V3.2",
			Description:         "DeepSeek V3.2 via CodeArts",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "glm-4-7-internal",
			Object:              "model",
			Created:             now,
			OwnedBy:             "huaweicloud",
			Type:                "codearts",
			DisplayName:         "GLM-4.7 Internal",
			Description:         "GLM-4.7 Internal via CodeArts",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "GLM-4-7-SFT-Harmony",
			Object:              "model",
			Created:             now,
			OwnedBy:             "huaweicloud",
			Type:                "codearts",
			DisplayName:         "GLM-4.7 SFT Harmony",
			Description:         "GLM-4.7 SFT Harmony via CodeArts",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
	}
}

// GetGitlabModels returns the standard GitLab model definitions.
// GitLab Duo Agent Platform supports multiple Claude and GPT-5 models.
// See: https://docs.gitlab.com/user/duo_agent_platform/model_selection/
func GetGitlabModels() []*ModelInfo {
	now := int64(1748044800) // 2025-05-24
	return []*ModelInfo{
		// Base model
		{
			ID:                  "gitlab-duo",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo",
			Description:         "GitLab Duo base model",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		// Claude Opus models
		{
			ID:                  "duo-chat-opus-4-6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (Claude Opus 4.6)",
			Description:         "Claude Opus 4.6 via GitLab Duo",
			ContextLength:       1000000,
			MaxCompletionTokens: 128000,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high", "max"}},
		},
		{
			ID:                  "duo-chat-opus-4-5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (Claude Opus 4.5)",
			Description:         "Claude Opus 4.5 via GitLab Duo",
			ContextLength:       1000000,
			MaxCompletionTokens: 128000,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high", "max"}},
		},
		// Claude Sonnet models
		{
			ID:                  "duo-chat-sonnet-4-6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (Claude Sonnet 4.6)",
			Description:         "Claude Sonnet 4.6 via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "duo-chat-sonnet-4-5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (Claude Sonnet 4.5)",
			Description:         "Claude Sonnet 4.5 via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		// Claude Haiku models
		{
			ID:                  "duo-chat-haiku-4-5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (Claude Haiku 4.5)",
			Description:         "Claude Haiku 4.5 via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "duo-chat-haiku-4-6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo Alias",
			Description:         "GitLab Duo Alias (Haiku 4.6)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		// GPT-5 models
		{
			ID:                  "duo-chat-gpt-5-1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (GPT-5.1)",
			Description:         "GPT-5.1 via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "duo-chat-gpt-5-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (GPT-5 Mini)",
			Description:         "GPT-5 Mini via GitLab Duo",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "duo-chat-gpt-5-2",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (GPT-5.2)",
			Description:         "GPT-5.2 via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "duo-chat-gpt-5-2-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (GPT-5.2 Codex)",
			Description:         "GPT-5.2 Codex via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "duo-chat-gpt-5-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "gitlab",
			Type:                "gitlab",
			DisplayName:         "GitLab Duo (GPT-5 Codex)",
			Description:         "GPT-5 Codex via GitLab Duo",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
	}
}

// GetQoderModels returns the available models for the Qoder provider.
func GetQoderModels() []*ModelInfo {
	now := int64(1748044800) // 2025-05-24
	return []*ModelInfo{
		{
			ID:          "auto",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Auto",
			Description: "Automatic model selection",
		},
		{
			ID:          "ultimate",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Ultimate",
			Description: "Qoder Ultimate tier model",
		},
		{
			ID:          "performance",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Performance",
			Description: "Qoder Performance tier model",
		},
		{
			ID:          "efficient",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Efficient",
			Description: "Qoder Efficient tier model",
		},
		{
			ID:          "lite",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Lite",
			Description: "Qoder Lite tier model",
		},
		{
			ID:          "qmodel_latest",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Qwen3.7-Max",
			Description: "Qwen 3.7 Max via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "qmodel",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Qwen3.7-Plus",
			Description: "Qwen 3.7 Plus via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "dmodel",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "DeepSeek-V4-Pro",
			Description: "DeepSeek V4 Pro via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "dfmodel",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "DeepSeek-V4-Flash",
			Description: "DeepSeek V4 Flash via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"high", "max"}},
		},
		{
			ID:          "gm51model",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "GLM-5.2",
			Description: "GLM 5.2 via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "kmodel_latest",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Kimi-K3",
			Description: "Kimi K3 via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "kmodel",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "Kimi-K2.7-Code",
			Description: "Kimi K2.7 Code via Qoder",
			Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "mmodel",
			Object:      "model",
			Created:     now,
			OwnedBy:     "qoder",
			Type:        "qoder",
			DisplayName: "MiniMax-M3",
			Description: "MiniMax M3 via Qoder",
		},
	}
}

// GetCodeBuddyModels returns the available models for CodeBuddy (Tencent).
// These models are served through the copilot.tencent.com API.
func GetCodeBuddyModels() []*ModelInfo {
	now := int64(1748044800)
	return []*ModelInfo{
		{
			ID: "auto", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Auto", Description: "Automatic model selection via CodeBuddy",
			ContextLength: 168000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "hy3-preview", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Hy3 Preview", Description: "Hunyuan thinking model with enhanced reasoning capabilities via CodeBuddy",
			ContextLength: 192000, MaxCompletionTokens: 64000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID: "glm-5v-turbo", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "GLM-5v Turbo", Description: "Native multimodal model via CodeBuddy",
			ContextLength: 200000, MaxCompletionTokens: 38000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "glm-5.2", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "GLM-5.2", Description: "GLM-5.2 via CodeBuddy",
			ContextLength: 200000, MaxCompletionTokens: 48000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID: "glm-5.1", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "GLM-5.1", Description: "GLM-5.1 via CodeBuddy",
			ContextLength: 200000, MaxCompletionTokens: 48000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID: "glm-5.0-turbo", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "GLM-5.0 Turbo", Description: "GLM-5.0 Turbo via CodeBuddy",
			ContextLength: 200000, MaxCompletionTokens: 48000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID: "glm-4.6", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "GLM-4.6", Description: "GLM-4.6 via CodeBuddy",
			ContextLength: 128000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID: "kimi-k2.7", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Kimi K2.7", Description: "Kimi K2.7 via CodeBuddy",
			ContextLength: 256000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "kimi-k2.6", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Kimi K2.6", Description: "Kimi K2.6 via CodeBuddy",
			ContextLength: 256000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "kimi-k2.5", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Kimi K2.5", Description: "Kimi K2.5 via CodeBuddy",
			ContextLength: 256000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "kimi-k2.5-v", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "Kimi-K2.5-V", Description: "Kimi K2.5-V via CodeBuddy",
			ContextLength: 256000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities: []string{"TEXT", "IMAGE"},
		},
		{
			ID: "deepseek-v4-flash", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "DeepSeek V4 Flash", Description: "DeepSeek V4 Flash via CodeBuddy",
			ContextLength: 1000000, MaxCompletionTokens: 50000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"high", "max"}},
		},
		{
			ID: "deepseek-v4-pro", Object: "model", Created: now, OwnedBy: "tencent",
			Type: "codebuddy", DisplayName: "DeepSeek V4 Pro", Description: "DeepSeek V4 Pro via CodeBuddy",
			ContextLength: 128000, MaxCompletionTokens: 32000, SupportedEndpoints: []string{"/chat/completions"},
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
	}
}

// GetCodeBuddyAIModels returns the available models for CodeBuddy AI (www.codebuddy.ai IDE).
func GetCodeBuddyAIModels() []*ModelInfo {
	now := int64(1748044800)
	return []*ModelInfo{
		{
			ID: "default-model", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "Default Model", Description: "Default model via CodeBuddy AI",
			ContextLength: 128000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "glm-5v-turbo", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GLM-5v Turbo", Description: "GLM-5v Turbo via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "kimi-k2.5", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "Kimi-K2.5", Description: "Kimi K2.5 via CodeBuddy AI",
			ContextLength: 256000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.4", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.4", Description: "GPT-5.4 via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.3-codex", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.3-Codex", Description: "GPT-5.3 Codex via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.2-codex", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.2-Codex", Description: "GPT-5.2 Codex via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.2", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.2", Description: "GPT-5.2 via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.1", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.1", Description: "GPT-5.1 via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gpt-5.1-codex-max", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "GPT-5.1-Codex-Max", Description: "GPT-5.1 Codex Max via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gemini-3.0-pro", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "Gemini-3.0-Pro", Description: "Gemini 3.0 Pro via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "gemini-3.0-flash", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "Gemini-3.0-Flash", Description: "Gemini 3.0 Flash via CodeBuddy AI",
			ContextLength: 200000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "deepseek-v3.2", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "DeepSeek-V3.2", Description: "DeepSeek V3.2 via CodeBuddy AI",
			ContextLength: 128000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
		{
			ID: "auto-chat", Object: "model", Created: now, OwnedBy: "codebuddy-ai",
			Type: "codebuddy-ai", DisplayName: "Auto-Chat", Description: "Auto Chat via CodeBuddy AI",
			ContextLength: 128000, MaxCompletionTokens: 32768, SupportedEndpoints: []string{"/chat/completions"},
		},
	}
}

// GetXAIModels returns the standard xAI Grok model definitions.
func GetXAIModels() []*ModelInfo {
	return WithXAIBuiltins(cloneModelInfos(getModels().XAI))
}

// WithCodexBuiltins injects hard-coded Codex-only model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithCodexBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, codexBuiltinImageModelInfo())
}

// WithXAIBuiltins injects hard-coded xAI image/video model definitions that should
// not depend on remote models.json updates.
func WithXAIBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, xaiBuiltinImageModelInfo(), xaiBuiltinImageQualityModelInfo(), xaiBuiltinVideoModelInfo())
}

func codexBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImageModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2",
		Version:     codexBuiltinImageModelID,
	}
}

func xaiBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image",
		Name:        xaiBuiltinImageModelID,
		Description: "xAI Grok image generation model.",
	}
}

func xaiBuiltinImageQualityModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageQualityModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image Quality",
		Name:        xaiBuiltinImageQualityModelID,
		Description: "xAI Grok higher-fidelity image generation model.",
	}
}

func xaiBuiltinVideoModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideoModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video",
		Name:        xaiBuiltinVideoModelID,
		Description: "xAI Grok video generation model.",
	}
}

func upsertModelInfos(models []*ModelInfo, extras ...*ModelInfo) []*ModelInfo {
	if len(extras) == 0 {
		return models
	}

	extraIDs := make(map[string]struct{}, len(extras))
	extraList := make([]*ModelInfo, 0, len(extras))
	for _, extra := range extras {
		if extra == nil {
			continue
		}
		id := strings.TrimSpace(extra.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := extraIDs[key]; exists {
			continue
		}
		extraIDs[key] = struct{}{}
		extraList = append(extraList, cloneModelInfo(extra))
	}

	if len(extraList) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models)+len(extraList))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := extraIDs[strings.ToLower(id)]; exists {
			continue
		}
		filtered = append(filtered, model)
	}

	filtered = append(filtered, extraList...)
	return filtered
}

// cloneModelInfos returns a shallow copy of the slice with each element deep-cloned.
func cloneModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, len(models))
	for i, m := range models {
		out[i] = cloneModelInfo(m)
	}
	return out
}

// GetStaticModelDefinitionsByChannel returns static model definitions for a given channel/provider.
// It returns nil when the channel is unknown.
//
// Supported channels:
//   - claude
//   - gemini
//   - vertex
//   - gemini-cli
//   - aistudio
//   - codex
//   - kimi
//   - kilo
//   - github-copilot
//   - amazonq
//   - antigravity (returns static overrides only)
//   - bt (BaoTa Panel; dynamically fetched with static fallbacks)
//   - xai
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "claude":
		return GetClaudeModels()
	case "gemini":
		return GetGeminiModels()
	case "vertex":
		return GetGeminiVertexModels()
	case "gemini-cli":
		return GetGeminiCLIModels()
	case "aistudio":
		return GetAIStudioModels()
	case "codex":
		return GetCodexProModels()
	case "kimi":
		return GetKimiModels()
	case "github-copilot":
		return GetGitHubCopilotModels()
	case "kiro":
		return GetKiroModels()
	case "kilo":
		return GetKiloModels()
	case "amazonq":
		return GetAmazonQModels()
	case "antigravity":
		return GetAntigravityModels()
	case "qoder":
		return GetQoderModels()
	case "bt":
		return GetBTModels()
	case "codebuddy":
		return GetCodeBuddyModels()
	case "codebuddy-ai":
		return GetCodeBuddyAIModels()
	case "cursor":
		return GetCursorModels()
	case "codearts":
		return GetCodeArtsModels()
	case "gitlab":
		return GetGitlabModels()
	case "xai", "x-ai", "grok":
		return GetXAIModels()
	default:
		return nil
	}
}

// GetBTModels returns the BaoTa (BT Panel) model definitions.
// Models are dynamically fetched at runtime via FetchBTModels; these static
// entries provide fallback metadata for model listing and alias configuration.
func GetBTModels() []*ModelInfo {
	now := int64(1745548800) // 2025-04-25
	return []*ModelInfo{
		{ID: "ernie-4.5-21b-a3b-thinking", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "ERNIE 4.5 21B A3B Thinking", Description: "Baidu ERNIE 4.5 thinking model via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192, Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
		{ID: "ernie-x1-turbo-32k-preview", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "ERNIE X1 Turbo 32K Preview", Description: "Baidu ERNIE X1 Turbo via BaoTa", ContextLength: 32768, MaxCompletionTokens: 8192},
		{ID: "ernie-x1.1", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "ERNIE X1.1", Description: "Baidu ERNIE X1.1 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "ernie-5.0", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "ERNIE 5.0", Description: "Baidu ERNIE 5.0 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "ernie-5.0-thinking-preview", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "ERNIE 5.0 Thinking Preview", Description: "Baidu ERNIE 5.0 thinking model via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384, Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
		{ID: "hunyuan-2.0-instruct-20251111", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Hunyuan 2.0 Instruct", Description: "Tencent Hunyuan 2.0 Instruct via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "hunyuan-2.0-thinking-20251109", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Hunyuan 2.0 Thinking", Description: "Tencent Hunyuan 2.0 thinking model via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192, Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
		{ID: "deepseek-r1-250528", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "DeepSeek R1 250528", Description: "DeepSeek R1 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384, Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
		{ID: "deepseek-v3-2-251201", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "DeepSeek V3.2 251201", Description: "DeepSeek V3.2 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "glm-4-7-251222", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "GLM-4.7 251222", Description: "Zhipu GLM-4.7 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "doubao-seed-2-0-code-preview-260215", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Doubao Seed 2.0 Code Preview", Description: "ByteDance Doubao Seed 2.0 Code via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "doubao-seed-2-0-mini-260215", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Doubao Seed 2.0 Mini", Description: "ByteDance Doubao Seed 2.0 Mini via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "doubao-seed-2-0-lite-260215", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Doubao Seed 2.0 Lite", Description: "ByteDance Doubao Seed 2.0 Lite via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "doubao-seed-2-0-pro-260215", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Doubao Seed 2.0 Pro", Description: "ByteDance Doubao Seed 2.0 Pro via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "text-embedding-v4", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Text Embedding V4", Description: "Baidu Text Embedding V4 via BaoTa"},
		{ID: "kimi-k2.5", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Kimi K2.5", Description: "Moonshot Kimi K2.5 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "deepseek-v3.2", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "DeepSeek V3.2", Description: "DeepSeek V3.2 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen-max-2025-01-25", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen Max 2025-01-25", Description: "Alibaba Qwen Max via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "glm-5", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "GLM-5", Description: "Zhipu GLM-5 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "qwen-flash", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen Flash", Description: "Alibaba Qwen Flash via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen-plus", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen Plus", Description: "Alibaba Qwen Plus via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "doubao-seed-1-8-251228", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Doubao Seed 1.8 251228", Description: "ByteDance Doubao Seed 1.8 via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen-plus-2025-12-01", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen Plus 2025-12-01", Description: "Alibaba Qwen Plus via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "deepseek-v4-flash", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "DeepSeek V4 Flash", Description: "DeepSeek V4 Flash via BaoTa", ContextLength: 1000000, MaxCompletionTokens: 384000},
		{ID: "deepseek-v4-pro", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "DeepSeek V4 Pro", Description: "DeepSeek V4 Pro via BaoTa", ContextLength: 1000000, MaxCompletionTokens: 384000},
		{ID: "qwen3.5-plus", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3.5 Plus", Description: "Alibaba Qwen3.5 Plus via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen3.5-flash", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3.5 Flash", Description: "Alibaba Qwen3.5 Flash via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen3-coder-flash", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3 Coder Flash", Description: "Alibaba Qwen3 Coder Flash via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen3-coder-plus", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3 Coder Plus", Description: "Alibaba Qwen3 Coder Plus via BaoTa", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "qwen3.6-plus", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3.6 Plus", Description: "Alibaba Qwen3.6 Plus via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen3-max", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3 Max", Description: "Alibaba Qwen3 Max via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
		{ID: "qwen3-max-2026-01-23", Object: "model", Created: now, OwnedBy: "bt", Type: "bt", DisplayName: "Qwen3 Max 2026-01-23", Description: "Alibaba Qwen3 Max via BaoTa", ContextLength: 128000, MaxCompletionTokens: 8192},
	}
}

// GetCursorModels returns the fallback Cursor model definitions.
func GetCursorModels() []*ModelInfo {
	return []*ModelInfo{
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
}

func GetGLMStaticModels() []*ModelInfo {
	now := int64(1748044800)
	return []*ModelInfo{
		{
			ID:                  "glm-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "zhipu",
			Type:                "glm",
			DisplayName:         "GLM-4.6",
			Description:         "GLM-4.6 static model definition",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
	}
}

// LookupStaticModelInfo searches all static model definitions for a model by ID.
// Returns nil if no matching model is found.
func LookupStaticModelInfo(modelID string) *ModelInfo {
	if modelID == "" {
		return nil
	}

	data := getModels()
	allModels := [][]*ModelInfo{
		data.Claude,
		data.Gemini,
		data.Vertex,
		data.GeminiCLI,
		data.AIStudio,
		data.CodexPro,
		data.Kimi,
		data.Antigravity,
		GetQoderModels(),
		GetGitHubCopilotModels(),
		GetKiroModels(),
		GetKiloModels(),
		GetAmazonQModels(),
		GetCodeBuddyModels(),
		GetCodeBuddyAIModels(),
		GetCodeArtsModels(),
		GetGLMStaticModels(),
		GetGitlabModels(),
		GetCursorModels(),
		data.XAI,
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && m.ID == modelID {
				return cloneModelInfo(m)
			}
		}
	}

	return nil
}

// defaultCopilotClaudeContextLength is the conservative prompt token limit for
// Claude models accessed via the GitHub Copilot API. Individual accounts are
// capped at 128K; business accounts at 168K. When the dynamic /models API fetch
// succeeds, the real per-account limit overrides this value. This constant is
// only used as a safe fallback.
const defaultCopilotClaudeContextLength = 128000

// GetGitHubCopilotModels returns the full union of GitHub Copilot model definitions.
// These models are available through the GitHub Copilot API at api.githubcopilot.com.
//
// Use the per-tier helpers (GetGitHubCopilotFreeModels, GetGitHubCopilotProModels,
// GetGitHubCopilotProPlusModels, GetGitHubCopilotMaxModels) to filter by plan tier.
func GetGitHubCopilotModels() []*ModelInfo {
	now := int64(1732752000) // 2024-11-27
	copilotClaudeEndpoints := []string{"/chat/completions", "/messages"}

	return []*ModelInfo{
		// --- Free tier ---
		{
			ID:                  "gpt-5-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5 Mini",
			Description:         "OpenAI GPT-5 Mini via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-haiku-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Haiku 4.5",
			Description:         "Anthropic Claude Haiku 4.5 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
		},
		// --- Pro tier additions ---
		{
			ID:                  "gpt-5.4-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.4 mini",
			Description:         "OpenAI GPT-5.4 mini via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "claude-sonnet-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.5",
			Description:         "Anthropic Claude Sonnet 4.5 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-sonnet-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.6",
			Description:         "Anthropic Claude Sonnet 4.6 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5.2",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.2",
			Description:         "OpenAI GPT-5.2 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.4",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.4",
			Description:         "OpenAI GPT-5.4 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.3-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.3 Codex",
			Description:         "OpenAI GPT-5.3 Codex via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		// --- Pro+ tier additions ---
		{
			ID:                  "gpt-5.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.5",
			Description:         "OpenAI GPT-5.5 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "claude-opus-4.7",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.7",
			Description:         "Anthropic Claude Opus 4.7 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-opus-4.8",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.8",
			Description:         "Anthropic Claude Opus 4.8 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		// --- Max tier additions ---
		{
			ID:                  "claude-opus-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.6",
			Description:         "Anthropic Claude Opus 4.6 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
	}
}

// GitHub Copilot plan tier model allowlists.
// Higher tiers are supersets of lower tiers, mirroring the Codex tier model.
// Tier identifiers map to GitHub Copilot plan types: free / pro / pro+ / max.

// copilotFreeModelIDs lists the model IDs available to the GitHub Copilot Free plan.
var copilotFreeModelIDs = []string{
	"gpt-5-mini",
	"claude-haiku-4.5",
}

// copilotProModelIDs lists the model IDs available to the GitHub Copilot Pro plan.
// Pro is a superset of Free.
var copilotProModelIDs = []string{
	"gpt-5-mini",
	"claude-haiku-4.5",
	"gpt-5.4-mini",
	"claude-sonnet-4.5",
	"claude-sonnet-4.6",
	"gpt-5.2",
	"gpt-5.4",
	"gpt-5.3-codex",
}

// copilotProPlusModelIDs lists the model IDs available to the GitHub Copilot Pro+ plan.
// Pro+ is a superset of Pro.
var copilotProPlusModelIDs = []string{
	"gpt-5-mini",
	"claude-haiku-4.5",
	"gpt-5.4-mini",
	"claude-sonnet-4.5",
	"claude-sonnet-4.6",
	"gpt-5.2",
	"gpt-5.4",
	"gpt-5.3-codex",
	"gpt-5.5",
	"claude-opus-4.7",
	"claude-opus-4.8",
}

// copilotMaxModelIDs lists the model IDs available to the GitHub Copilot Max plan.
// Max is a superset of Pro+.
var copilotMaxModelIDs = []string{
	"gpt-5-mini",
	"claude-haiku-4.5",
	"gpt-5.4-mini",
	"claude-sonnet-4.5",
	"claude-sonnet-4.6",
	"gpt-5.2",
	"gpt-5.4",
	"gpt-5.3-codex",
	"gpt-5.5",
	"claude-opus-4.7",
	"claude-opus-4.8",
	"claude-opus-4.6",
}

// filterGitHubCopilotModelsByIDs returns a copy of allModels filtered to only those
// whose ID appears in the allowlist (case-insensitive, whitespace-trimmed).
func filterGitHubCopilotModelsByIDs(allModels []*ModelInfo, allowlist []string) []*ModelInfo {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, id := range allowlist {
		allowed[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	out := make([]*ModelInfo, 0, len(allowlist))
	for _, m := range allModels {
		if m == nil {
			continue
		}
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(m.ID))]; ok {
			out = append(out, cloneModelInfo(m))
		}
	}
	return out
}

// GetGitHubCopilotFreeModels returns the model definitions available to the
// GitHub Copilot Free plan tier.
func GetGitHubCopilotFreeModels() []*ModelInfo {
	return filterGitHubCopilotModelsByIDs(GetGitHubCopilotModels(), copilotFreeModelIDs)
}

// GetGitHubCopilotProModels returns the model definitions available to the
// GitHub Copilot Pro plan tier. Pro is a superset of Free.
func GetGitHubCopilotProModels() []*ModelInfo {
	return filterGitHubCopilotModelsByIDs(GetGitHubCopilotModels(), copilotProModelIDs)
}

// GetGitHubCopilotProPlusModels returns the model definitions available to the
// GitHub Copilot Pro+ plan tier. Pro+ is a superset of Pro.
func GetGitHubCopilotProPlusModels() []*ModelInfo {
	return filterGitHubCopilotModelsByIDs(GetGitHubCopilotModels(), copilotProPlusModelIDs)
}

// GetGitHubCopilotMaxModels returns the model definitions available to the
// GitHub Copilot Max plan tier. Max is a superset of Pro+.
func GetGitHubCopilotMaxModels() []*ModelInfo {
	return filterGitHubCopilotModelsByIDs(GetGitHubCopilotModels(), copilotMaxModelIDs)
}

// GetKiroModels returns the Kiro (AWS CodeWhisperer) model definitions
func GetKiroModels() []*ModelInfo {
	return []*ModelInfo{
		// --- Base Models ---
		{
			ID:                  "kiro-auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Auto",
			Description:         "Automatic model selection by Kiro",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-opus-4-6",
			Object:              "model",
			Created:             1736899200, // 2025-01-15
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.6",
			Description:         "Claude Opus 4.6 via Kiro (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-6",
			Object:              "model",
			Created:             1739836800, // 2025-02-18
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.6",
			Description:         "Claude Sonnet 4.6 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-opus-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.5",
			Description:         "Claude Opus 4.5 via Kiro (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.5",
			Description:         "Claude Sonnet 4.5 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4",
			Description:         "Claude Sonnet 4 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-haiku-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Haiku 4.5",
			Description:         "Claude Haiku 4.5 via Kiro (0.4x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		// --- 第三方模型 (通过 Kiro 接入) ---
		{
			ID:                  "kiro-deepseek-3-2",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro DeepSeek 3.2",
			Description:         "DeepSeek 3.2 via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-minimax-m2-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro MiniMax M2.5",
			Description:         "MiniMax M2.5 via Kiro",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-minimax-m2-1",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro MiniMax M2.1",
			Description:         "MiniMax M2.1 via Kiro",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-glm-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GLM-5",
			Description:         "GLM-5 via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-qwen3-coder-next",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Qwen3 Coder Next",
			Description:         "Qwen3 Coder Next via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		// --- Agentic Variants (Optimized for coding agents with chunked writes) ---
		{
			ID:                  "kiro-claude-sonnet-4-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.5 (Agentic)",
			Description:         "Claude Sonnet 4.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4 (Agentic)",
			Description:         "Claude Sonnet 4 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-haiku-4-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Haiku 4.5 (Agentic)",
			Description:         "Claude Haiku 4.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-deepseek-3-2-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro DeepSeek 3.2 (Agentic)",
			Description:         "DeepSeek 3.2 optimized for coding agents (chunked writes)",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-minimax-m2-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro MiniMax M2.5 (Agentic)",
			Description:         "MiniMax M2.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-minimax-m2-1-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro MiniMax M2.1 (Agentic)",
			Description:         "MiniMax M2.1 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-glm-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GLM-5 (Agentic)",
			Description:         "GLM-5 optimized for coding agents (chunked writes)",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-qwen3-coder-next-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Qwen3 Coder Next (Agentic)",
			Description:         "Qwen3 Coder Next optimized for coding agents (chunked writes)",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetAmazonQModels returns the Amazon Q (AWS CodeWhisperer) model definitions.
// These models use the same API as Kiro and share the same executor.
func GetAmazonQModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                  "amazonq-auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro", // Uses Kiro executor - same API
			DisplayName:         "Amazon Q Auto",
			Description:         "Automatic model selection by Amazon Q",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-opus-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Opus 4.5",
			Description:         "Claude Opus 4.5 via Amazon Q (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-sonnet-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Sonnet 4.5",
			Description:         "Claude Sonnet 4.5 via Amazon Q (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-sonnet-4",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Sonnet 4",
			Description:         "Claude Sonnet 4 via Amazon Q (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-haiku-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Haiku 4.5",
			Description:         "Claude Haiku 4.5 via Amazon Q (0.4x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	}
}
