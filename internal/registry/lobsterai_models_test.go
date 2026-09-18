package registry

import (
	"strings"
	"testing"
)

func TestGetLobsterAIModelsDeclaresChatEndpoint(t *testing.T) {
	models := GetLobsterAIModels()
	if len(models) == 0 {
		t.Fatal("GetLobsterAIModels returned no models")
	}
	for _, model := range models {
		if model == nil {
			t.Fatal("catalog contains a nil model")
		}
		if model.ID == "" {
			t.Fatal("catalog entry has an empty ID")
		}
		if model.Type != "lobsterai" {
			t.Fatalf("model %q Type = %q, want lobsterai", model.ID, model.Type)
		}
		// The upstream only serves /chat/completions; declaring it lets the
		// router bridge /v1/responses through the chat format.
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q endpoints = %#v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
		if model.ContextLength <= 0 {
			t.Fatalf("model %q context length = %d, want a positive value", model.ID, model.ContextLength)
		}
	}
}

func TestGetLobsterAIModelsThinkingLevels(t *testing.T) {
	thinkingModel := LookupLobsterAIModel("deepseek-v4-pro")
	if thinkingModel == nil {
		t.Fatal("deepseek-v4-pro is missing from the catalog")
	}
	if thinkingModel.Thinking == nil {
		t.Fatal("deepseek-v4-pro has no thinking support")
	}
	if len(thinkingModel.Thinking.Levels) == 0 {
		t.Fatal("deepseek-v4-pro has no reasoning levels")
	}
	if strings.Join(thinkingModel.Thinking.Levels, ",") != "minimal,low,medium,high,xhigh,max" {
		t.Fatalf("levels = %#v, want the official client level set", thinkingModel.Thinking.Levels)
	}

	// The qwen models are served without reasoning control.
	if qwen := LookupLobsterAIModel("qwen3.7-max"); qwen == nil {
		t.Fatal("qwen3.7-max is missing from the catalog")
	} else if qwen.Thinking != nil {
		t.Fatalf("qwen3.7-max thinking = %#v, want nil", qwen.Thinking)
	}
}

func TestLookupLobsterAIModelAcceptsNamespacedIDs(t *testing.T) {
	for _, id := range []string{"glm-5", "GLM-5", "lobsterai/glm-5", "LobsterAI/GLM-5"} {
		if model := LookupLobsterAIModel(id); model == nil || model.ID != "glm-5" {
			t.Fatalf("LookupLobsterAIModel(%q) = %#v, want glm-5", id, model)
		}
	}
	if model := LookupLobsterAIModel(""); model != nil {
		t.Fatalf("LookupLobsterAIModel(\"\") = %#v, want nil", model)
	}
	if model := LookupLobsterAIModel("not-a-real-model"); model != nil {
		t.Fatalf("LookupLobsterAIModel(unknown) = %#v, want nil", model)
	}
}

func TestGetLobsterAIModelsIsolatesCallerMutations(t *testing.T) {
	models := GetLobsterAIModels()
	models[0].ID = "mutated"
	models[0].SupportedEndpoints[0] = "/mutated"

	fresh := GetLobsterAIModels()
	if fresh[0].ID == "mutated" {
		t.Fatal("GetLobsterAIModels returned shared catalog storage")
	}
	if fresh[0].SupportedEndpoints[0] != "/chat/completions" {
		t.Fatal("GetLobsterAIModels returned a shared endpoints slice")
	}
}

func TestGetStaticModelDefinitionsServesLobsterAI(t *testing.T) {
	for _, channel := range []string{"lobsterai", "lobster", "youdao", "LOBSTERAI"} {
		models := GetStaticModelDefinitionsByChannel(channel)
		if len(models) == 0 {
			t.Fatalf("GetStaticModelDefinitionsByChannel(%q) returned no models", channel)
		}
	}
}

// TestLobsterAIModelsStayOutOfTheProviderAgnosticLookup pins the isolation
// guarantee: the catalog shares model ids with other providers (qwen3.7-max is
// also a Qoder id), so it must only resolve through the provider-scoped
// registry. Resolving it in LookupStaticModelInfo would leak LobsterAI thinking
// metadata into every other provider that uses the same id.
func TestLobsterAIModelsStayOutOfTheProviderAgnosticLookup(t *testing.T) {
	for _, sharedID := range []string{"qwen3.7-max", "kimi-k2.6", "glm-5", "deepseek-v4-pro"} {
		if model := LookupStaticModelInfo(sharedID); model != nil && model.Type == "lobsterai" {
			t.Fatalf("LookupStaticModelInfo(%q) resolved a LobsterAI catalog entry", sharedID)
		}
	}
	// The provider-scoped channel accessor must still serve the catalog.
	if models := GetStaticModelDefinitionsByChannel("lobsterai"); len(models) == 0 {
		t.Fatal("the lobsterai channel returned no models")
	}
}
