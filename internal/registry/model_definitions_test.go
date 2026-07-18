package registry

import (
	"slices"
	"strings"
	"testing"
)

func TestGeminiVertexModelsUseProductionReleaseIDs(t *testing.T) {
	releaseIDs := []string{
		"gemini-3-pro",
		"gemini-3-flash",
		"gemini-3.1-pro",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-lite",
		"gemini-3-pro-image",
	}
	found := make(map[string]bool, len(releaseIDs))
	for _, model := range GetGeminiVertexModels() {
		if model == nil {
			continue
		}
		if strings.HasSuffix(model.ID, "-preview") && strings.HasPrefix(model.ID, "gemini-3") {
			t.Fatalf("Vertex model ID = %q still uses preview suffix", model.ID)
		}
		for _, releaseID := range releaseIDs {
			if model.ID == releaseID {
				found[releaseID] = true
			}
		}
	}
	for _, releaseID := range releaseIDs {
		if !found[releaseID] {
			t.Fatalf("Vertex models do not contain %q", releaseID)
		}
	}
}

func TestGetKimiModelsIncludesK3FinalEntry(t *testing.T) {
	models := GetKimiModels()
	model := findModelInfo(models, "kimi-k3")
	if model == nil {
		t.Fatal("expected kimi-k3 in GetKimiModels()")
	}
	if model.Type != "kimi" {
		t.Fatalf("type = %q, want kimi", model.Type)
	}
	if model.ContextLength != 262144 {
		t.Fatalf("context_length = %d, want 262144", model.ContextLength)
	}
	if model.MaxCompletionTokens != 65536 {
		t.Fatalf("max_completion_tokens = %d, want 65536", model.MaxCompletionTokens)
	}
	if model.Thinking == nil {
		t.Fatal("thinking support is nil")
	}
	if model.Thinking.ZeroAllowed {
		t.Fatal("zero_allowed = true, want false")
	}
	if model.Thinking.DynamicAllowed {
		t.Fatal("dynamic_allowed = true, want false/absent")
	}
	if model.Thinking.Min != 0 || model.Thinking.Max != 0 {
		t.Fatalf("min/max = %d/%d, want 0/0 for level-only Kimi model", model.Thinking.Min, model.Thinking.Max)
	}
	wantLevels := []string{"low", "high", "max"}
	if !slices.Equal(model.Thinking.Levels, wantLevels) {
		t.Fatalf("levels = %v, want %v", model.Thinking.Levels, wantLevels)
	}
	if findModelInfo(models, "kimi-k3[1m]") != nil {
		t.Fatal("kimi-k3[1m] should not exist in final Kimi registry")
	}
}

func TestGetKimiModelsThinkingIsLevelOnly(t *testing.T) {
	models := GetKimiModels()
	for _, id := range []string{"kimi-k2-thinking", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "kimi-k2.7-code-highspeed"} {
		model := findModelInfo(models, id)
		if model == nil {
			t.Fatalf("expected model %q", id)
		}
		if model.Thinking == nil {
			t.Fatalf("%s thinking is nil", id)
		}
		if model.Thinking.Min != 0 || model.Thinking.Max != 0 || model.Thinking.DynamicAllowed {
			t.Fatalf("%s thinking = %#v, want level-only without min/max/dynamic_allowed", id, model.Thinking)
		}
		if !slices.Equal(model.Thinking.Levels, []string{"low", "high"}) {
			t.Fatalf("%s levels = %v, want [low high]", id, model.Thinking.Levels)
		}
	}
}

func TestGitHubCopilotClaudeModelsSupportMessages(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"claude-haiku-4.5":  false,
		"claude-opus-4.6":   false,
		"claude-opus-4.7":   false,
		"claude-opus-4.8":   false,
		"claude-sonnet-4.5": false,
		"claude-sonnet-4.6": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if !containsString(model.SupportedEndpoints, "/chat/completions") {
			t.Fatalf("model %q supported endpoints = %v, missing /chat/completions", model.ID, model.SupportedEndpoints)
		}
		if !containsString(model.SupportedEndpoints, "/messages") {
			t.Fatalf("model %q supported endpoints = %v, missing /messages", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}

func containsString(items []string, want string) bool {
	return slices.Contains(items, want)
}

func TestCodexFreeModelsExcludeGPT55(t *testing.T) {
	model := findModelInfo(GetCodexFreeModels(), "gpt-5.5")
	if model != nil {
		t.Fatal("expected codex free tier to NOT include gpt-5.5")
	}
}

func TestCodexStaticModelsIncludeGPT55(t *testing.T) {
	tierModels := map[string][]*ModelInfo{
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}

	for tier, models := range tierModels {
		t.Run(tier, func(t *testing.T) {
			model := findModelInfo(models, "gpt-5.5")
			if model == nil {
				t.Fatalf("expected codex %s tier to include gpt-5.5", tier)
			}
			assertGPT55ModelInfo(t, tier, model)
		})
	}

	model := LookupStaticModelInfo("gpt-5.5")
	if model == nil {
		t.Fatal("expected LookupStaticModelInfo to find gpt-5.5")
	}
	assertGPT55ModelInfo(t, "lookup", model)
}

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	got := ModelOverrideHeaders("gpt-5.6-luna")
	if got == nil {
		t.Fatal("ModelOverrideHeaders(gpt-5.6-luna) = nil, want headers")
	}
	if got["user-agent"] != wantUA {
		t.Fatalf("user-agent = %q, want %q", got["user-agent"], wantUA)
	}
	if got["originator"] != "codex-tui" {
		t.Fatalf("originator = %q, want codex-tui", got["originator"])
	}
	if got := ModelOverrideHeaders("gpt-5.4"); got != nil {
		t.Fatalf("ModelOverrideHeaders(gpt-5.4) = %#v, want nil", got)
	}
}

func TestWithXAIBuiltinsAddsVideoModel(t *testing.T) {
	models := WithXAIBuiltins(nil)
	found := false
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinVideoModelID {
			found = true
			if model.OwnedBy != "xai" {
				t.Fatalf("OwnedBy = %q, want xai", model.OwnedBy)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s builtin model", xaiBuiltinVideoModelID)
	}
}

func TestGitHubCopilotTierModelsContainment(t *testing.T) {
	tiers := map[string][]*ModelInfo{
		"free": GetGitHubCopilotFreeModels(),
		"pro":  GetGitHubCopilotProModels(),
		"pro+": GetGitHubCopilotProPlusModels(),
		"max":  GetGitHubCopilotMaxModels(),
		"all":  GetGitHubCopilotModels(),
	}

	// Every model in a lower tier must appear in every higher tier.
	mustContain := map[string][]string{
		"free": {},
		"pro": {
			"gpt-5-mini", "claude-haiku-4.5",
			"gpt-5.4-mini", "claude-sonnet-4.5", "claude-sonnet-4.6",
			"gpt-5.2", "gpt-5.4", "gpt-5.3-codex",
		},
		"pro+": {
			"gpt-5-mini", "claude-haiku-4.5",
			"gpt-5.4-mini", "claude-sonnet-4.5", "claude-sonnet-4.6",
			"gpt-5.2", "gpt-5.4", "gpt-5.3-codex",
			"gpt-5.5", "claude-opus-4.7", "claude-opus-4.8",
		},
		"max": {
			"gpt-5-mini", "claude-haiku-4.5",
			"gpt-5.4-mini", "claude-sonnet-4.5", "claude-sonnet-4.6",
			"gpt-5.2", "gpt-5.4", "gpt-5.3-codex",
			"gpt-5.5", "claude-opus-4.7", "claude-opus-4.8",
			"claude-opus-4.6",
		},
		"all": {
			"gpt-5-mini", "claude-haiku-4.5",
			"gpt-5.4-mini", "claude-sonnet-4.5", "claude-sonnet-4.6",
			"gpt-5.2", "gpt-5.4", "gpt-5.3-codex",
			"gpt-5.5", "claude-opus-4.7", "claude-opus-4.8",
			"claude-opus-4.6",
		},
	}

	for tierName, wantIDs := range mustContain {
		t.Run(tierName+"/contains-expected", func(t *testing.T) {
			models := tiers[tierName]
			for _, id := range wantIDs {
				if findModelInfo(models, id) == nil {
					t.Fatalf("%s tier missing required model %q", tierName, id)
				}
			}
		})
	}

	// Free tier must NOT include Pro+ and Max models.
	mustExclude := map[string][]string{
		"free": {
			"gpt-5.4-mini", "claude-sonnet-4.5", "claude-sonnet-4.6",
			"gpt-5.2", "gpt-5.4", "gpt-5.3-codex",
			"gpt-5.5", "claude-opus-4.7", "claude-opus-4.8", "claude-opus-4.6",
		},
		"pro": {
			"gpt-5.5", "claude-opus-4.7", "claude-opus-4.8", "claude-opus-4.6",
		},
		"pro+": {
			"claude-opus-4.6",
		},
	}

	for tierName, bannedIDs := range mustExclude {
		t.Run(tierName+"/excludes-higher-tier", func(t *testing.T) {
			models := tiers[tierName]
			for _, id := range bannedIDs {
				if findModelInfo(models, id) != nil {
					t.Fatalf("%s tier should NOT include model %q", tierName, id)
				}
			}
		})
	}

	// Returned slices must be independent copies; mutating one must not affect another.
	t.Run("returned-slices-are-independent", func(t *testing.T) {
		free := GetGitHubCopilotFreeModels()
		pro := GetGitHubCopilotProModels()
		if len(free) == 0 || len(pro) == 0 {
			t.Fatalf("expected non-empty tier slices, got free=%d pro=%d", len(free), len(pro))
		}
		originalID := free[0].ID
		free[0].ID = "mutated"
		defer func() { free[0].ID = originalID }()
		if findModelInfo(GetGitHubCopilotFreeModels(), originalID) == nil {
			t.Fatal("mutating returned slice affected subsequent calls")
		}
	})
}

func TestValidateModelsCatalogAllowsMissingSections(t *testing.T) {
	data := validTestModelsCatalog()
	data.XAI = nil

	if err := validateModelsCatalog(data); err != nil {
		t.Fatalf("validateModelsCatalog() error = %v", err)
	}
}

func TestValidateModelsCatalogRejectsInvalidDefinitions(t *testing.T) {
	data := validTestModelsCatalog()
	data.Claude = []*ModelInfo{{ID: ""}}

	if err := validateModelsCatalog(data); err == nil {
		t.Fatal("expected invalid model definition error")
	}
}

func validTestModelsCatalog() *staticModelsJSON {
	models := []*ModelInfo{{ID: "test-model"}}
	return &staticModelsJSON{
		Claude:      models,
		Gemini:      models,
		Vertex:      models,
		GeminiCLI:   models,
		AIStudio:    models,
		CodexFree:   models,
		CodexTeam:   models,
		CodexPlus:   models,
		CodexPro:    models,
		Kimi:        models,
		Antigravity: models,
		XAI:         models,
	}
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func assertGPT55ModelInfo(t *testing.T, source string, model *ModelInfo) {
	t.Helper()

	if model.ID != "gpt-5.5" {
		t.Fatalf("%s id mismatch: got %q", source, model.ID)
	}
	if model.Object != "model" {
		t.Fatalf("%s object mismatch: got %q", source, model.Object)
	}
	if model.Created != 1776902400 {
		t.Fatalf("%s created timestamp mismatch: got %d", source, model.Created)
	}
	if model.OwnedBy != "openai" {
		t.Fatalf("%s owned_by mismatch: got %q", source, model.OwnedBy)
	}
	if model.Type != "openai" {
		t.Fatalf("%s type mismatch: got %q", source, model.Type)
	}
	if model.DisplayName != "GPT 5.5" {
		t.Fatalf("%s display name mismatch: got %q", source, model.DisplayName)
	}
	if model.Version != "gpt-5.5" {
		t.Fatalf("%s version mismatch: got %q", source, model.Version)
	}
	if model.Description != "Frontier model for complex coding, research, and real-world work." {
		t.Fatalf("%s description mismatch: got %q", source, model.Description)
	}
	if model.ContextLength != 272000 {
		t.Fatalf("%s context length mismatch: got %d", source, model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("%s max completion tokens mismatch: got %d", source, model.MaxCompletionTokens)
	}
	if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("%s supported parameters mismatch: got %v", source, model.SupportedParameters)
	}
	if model.Thinking == nil {
		t.Fatalf("%s missing thinking support", source)
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("%s thinking level count mismatch: got %d, want %d", source, len(model.Thinking.Levels), len(want))
	}
	for i, level := range want {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("%s thinking level %d mismatch: got %q, want %q", source, i, model.Thinking.Levels[i], level)
		}
	}
}
