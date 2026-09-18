package registry

import "strings"

// lobsterAIDefaultContextLength is the context window the LobsterAI API
// advertises for its chat models.
const lobsterAIDefaultContextLength = 131072

// lobsterAIReasoningLevels mirrors the discrete reasoning efforts the official
// desktop client offers for LobsterAI models (KIMI_K3_RUNTIME_PROFILE).
var lobsterAIReasoningLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// lobsterAIModels is the LobsterAI (NetEase Youdao) catalog served by
// /api/models/available. The upstream list is dynamic and only reachable with a
// logged-in credential, so the catalog is compiled in and refreshed by editing
// this table (or the models.json "lobsterai" section) when the lineup changes.
var lobsterAIModels = []*ModelInfo{
	lobsterAIModel("deepseek-v4-pro", "DeepSeek V4 Pro", true, 0),
	lobsterAIModel("deepseek-v4-flash", "DeepSeek V4 Flash", true, 0),
	lobsterAIModel("MiniMax-M3", "MiniMax M3", true, 0),
	lobsterAIModel("MiniMax-M2.7", "MiniMax M2.7", true, 0),
	// qwen models are served without thinking control on the LobsterAI API.
	lobsterAIModel("qwen3.7-max", "Qwen3.7 Max", false, 0),
	lobsterAIModel("qwen3.7-plus", "Qwen3.7 Plus", false, 0),
	lobsterAIModel("qwen3.6-plus", "Qwen3.6 Plus", false, 0),
	lobsterAIModel("qwen3.5-plus-2026-04-20", "Qwen3.5 Plus", false, 0),
	lobsterAIModel("kimi-k3", "Kimi K3", true, 1048576),
	lobsterAIModel("kimi-k2.7-code", "Kimi K2.7 Code", true, 0),
	lobsterAIModel("kimi-k2.7-code-highspeed", "Kimi K2.7 Code Highspeed", true, 0),
	lobsterAIModel("kimi-k2.6", "Kimi K2.6", true, 262144),
	lobsterAIModel("kimi-k2.5", "Kimi K2.5", true, 262144),
	lobsterAIModel("doubao-seed-2-1-pro-260628", "Doubao Seed 2.1 Pro", true, 0),
	lobsterAIModel("doubao-seed-2-1-turbo-260628", "Doubao Seed 2.1 Turbo", true, 0),
	lobsterAIModel("doubao-seed-2-0-code-preview-260215", "Doubao Seed 2.0 Code Preview", true, 0),
	lobsterAIModel("glm-5.2", "GLM 5.2", true, 0),
	lobsterAIModel("glm-5.1", "GLM 5.1", true, 0),
	lobsterAIModel("glm-5v-turbo", "GLM 5V Turbo", true, 0),
	lobsterAIModel("glm-5", "GLM 5", true, 0),
}

// lobsterAIModel builds one catalog entry. Only /chat/completions is served
// upstream; declaring it lets the router bridge /v1/responses through the chat
// format instead of forwarding Responses payloads the upstream rejects.
func lobsterAIModel(id, displayName string, thinking bool, contextLength int) *ModelInfo {
	if contextLength <= 0 {
		contextLength = lobsterAIDefaultContextLength
	}
	info := &ModelInfo{
		ID:                 id,
		Object:             "model",
		Created:            1753600000,
		OwnedBy:            "lobsterai",
		Type:               "lobsterai",
		DisplayName:        displayName,
		ContextLength:      contextLength,
		SupportedEndpoints: []string{"/chat/completions"},
	}
	if thinking {
		info.Thinking = &ThinkingSupport{Levels: append([]string(nil), lobsterAIReasoningLevels...)}
	}
	return info
}

// GetLobsterAIModels returns the LobsterAI catalog, preferring a models.json
// override section when present. Returned entries are safe for callers to mutate.
func GetLobsterAIModels() []*ModelInfo {
	source := lobsterAIModels
	if m := getModels(); m != nil && len(m.LobsterAI) > 0 {
		source = m.LobsterAI
	}
	models := cloneModelInfos(source)
	// cloneModelInfo does not copy the endpoints slice, so the catalog would
	// otherwise share it with every caller.
	for _, model := range models {
		if model == nil || len(model.SupportedEndpoints) == 0 {
			continue
		}
		model.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	}
	return models
}

// LookupLobsterAIModel looks up a catalog entry by model id, accepting both the
// bare name and a "lobsterai/" prefixed name.
func LookupLobsterAIModel(modelID string) *ModelInfo {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "lobsterai/")
	if clean == "" {
		return nil
	}
	for _, model := range GetLobsterAIModels() {
		if model == nil {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(model.ID))
		if id == clean || strings.ToLower(strings.TrimPrefix(id, "lobsterai/")) == clean {
			return model
		}
	}
	return nil
}
