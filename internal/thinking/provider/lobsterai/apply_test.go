package lobsterai

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

func decodeEffort(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(body, &decoded); errUnmarshal != nil {
		t.Fatalf("decode body: %v", errUnmarshal)
	}
	value, exists := decoded["reasoning_effort"]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("reasoning_effort = %#v, want a string", value)
	}
	return text, true
}

func thinkingModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:       "glm-5",
		Type:     "lobsterai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
	}
}

func TestApplyWritesDiscreteLevel(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"glm-5","messages":[]}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{
		Mode:  thinking.ModeLevel,
		Level: thinking.LevelHigh,
	}, thinkingModel())
	if errApply != nil {
		t.Fatalf("Apply: %v", errApply)
	}
	effort, ok := decodeEffort(t, updated)
	if !ok || effort != "high" {
		t.Fatalf("reasoning_effort = %q, %v; want high", effort, ok)
	}
}

func TestApplyNoneRemovesEffortField(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"glm-5","reasoning_effort":"high"}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, thinkingModel())
	if errApply != nil {
		t.Fatalf("Apply: %v", errApply)
	}
	if _, exists := decodeEffort(t, updated); exists {
		t.Fatalf("body = %s, want reasoning_effort removed", updated)
	}
}

func TestApplyBudgetConvertsToLevel(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"glm-5"}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 4096,
	}, thinkingModel())
	if errApply != nil {
		t.Fatalf("Apply: %v", errApply)
	}
	effort, ok := decodeEffort(t, updated)
	if !ok || effort == "" {
		t.Fatalf("budget mode produced no level (body %s)", updated)
	}
}

func TestApplySkipsModelsWithoutReasoning(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"qwen3.7-max","reasoning_effort":"high"}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{
		Mode:  thinking.ModeLevel,
		Level: thinking.LevelHigh,
	}, &registry.ModelInfo{ID: "qwen3.7-max", Type: "lobsterai"})
	if errApply != nil {
		t.Fatalf("Apply: %v", errApply)
	}
	if string(updated) != string(body) {
		t.Fatalf("body = %s, want the payload untouched for a non-reasoning model", updated)
	}
}

func TestApplyTreatsUnknownModelsAsUserDefined(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"custom-model"}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{
		Mode:  thinking.ModeLevel,
		Level: thinking.LevelMedium,
	}, nil)
	if errApply != nil {
		t.Fatalf("Apply: %v", errApply)
	}
	effort, ok := decodeEffort(t, updated)
	if !ok || effort != "medium" {
		t.Fatalf("reasoning_effort = %q, %v; want medium for user-defined models", effort, ok)
	}
}
