package thinking

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestValidateConfigRejectsExplicitUnsupportedLevelForCompatibilityProvider(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:   "gpt-5",
		Type: "github-copilot",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}

	_, err := ValidateConfig(
		ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh},
		modelInfo,
		"openai",
		"openai",
		false,
	)
	if err == nil {
		t.Fatal("ValidateConfig() error = nil, want ErrLevelNotSupported")
	}
	var thinkingErr *ThinkingError
	if !errors.As(err, &thinkingErr) || thinkingErr.Code != ErrLevelNotSupported {
		t.Fatalf("ValidateConfig() error = %v, want ErrLevelNotSupported", err)
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
