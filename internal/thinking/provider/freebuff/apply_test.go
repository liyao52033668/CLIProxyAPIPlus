package freebuff

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
		ID:       "z-ai/glm-5.3-flash",
		Type:     "freebuff",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}},
	}
}

func TestApplyWritesDiscreteLevel(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"model":"z-ai/glm-5.3-flash","messages":[]}`)

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
	body := []byte(`{"model":"z-ai/glm-5.3-flash","reasoning_effort":"high"}`)

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
	body := []byte(`{"model":"z-ai/glm-5.3-flash"}`)

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
	body := []byte(`{"model":"upstage/solar-pro4","reasoning_effort":"high"}`)

	updated, errApply := applier.Apply(body, thinking.ThinkingConfig{
		Mode:  thinking.ModeLevel,
		Level: thinking.LevelHigh,
	}, &registry.ModelInfo{ID: "upstage/solar-pro4", Type: "freebuff"})
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
