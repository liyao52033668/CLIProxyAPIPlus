package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestValidateConfigClampsExplicitUnsupportedLevelForCompatibilityProvider(t *testing.T) {
	// Compatibility providers (github-copilot, kimi, ...) may reuse another wire format.
	// When model.Type is outside the from/to family, unsupported explicit levels clamp
	// to the nearest supported value instead of hard-failing. This is required for
	// Claude-compatible Kimi /v1/messages (effort=max -> high).
	modelInfo := &registry.ModelInfo{
		ID:   "gpt-5",
		Type: "github-copilot",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}

	got, err := ValidateConfig(
		ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh},
		modelInfo,
		"openai",
		"openai",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v, want clamped level", err)
	}
	if got.Mode != ModeLevel || got.Level != LevelHigh {
		t.Fatalf("ValidateConfig() = %#v, want level high", got)
	}
}

func TestValidateConfigClampsBudgetDerivedLevelForLevelOnlyModel(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:   "level-subset-model",
		Type: "github-copilot",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "high"},
		},
	}

	got, err := ValidateConfig(
		ThinkingConfig{Mode: ModeBudget, Budget: 32768},
		modelInfo,
		"claude",
		"openai",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got.Mode != ModeLevel || got.Level != LevelHigh {
		t.Fatalf("ValidateConfig() = %#v, want level high", got)
	}
}

func TestValidateConfigModeNoneFallsBackForClaudeCompatibleLevelModel(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:   "kimi-k2.5",
		Type: "kimi",
		Thinking: &registry.ThinkingSupport{
			Levels:      []string{"low", "high"},
			ZeroAllowed: false,
		},
	}

	got, err := ValidateConfig(
		ThinkingConfig{Mode: ModeNone},
		modelInfo,
		"claude",
		"claude",
		false,
	)
	if err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	if got.Mode != ModeNone || got.Level != LevelLow {
		t.Fatalf("ValidateConfig() = %#v, want ModeNone fallback level low", got)
	}
}
