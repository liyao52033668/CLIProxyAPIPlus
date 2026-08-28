package commandcode

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func commandCodeModelInfo() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:       "minimax/minimax-m3-free",
		Type:     "commandcode",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	}
}

func TestApply_ModeLevel_SetsParamsReasoningEffort(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"config":{},"params":{"model":"minimax/minimax-m3-free","stream":true}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, commandCodeModelInfo())
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "params.reasoning_effort").String(); got != "high" {
		t.Fatalf("params.reasoning_effort = %q, want %q, body=%s", got, "high", string(out))
	}
	if got := gjson.GetBytes(out, "params.model").String(); got != "minimax/minimax-m3-free" {
		t.Fatalf("params.model should be preserved, body=%s", string(out))
	}
}

func TestApply_ModeBudget_ConvertsToLevel(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"params":{"model":"minimax/minimax-m3-free"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 1024}, commandCodeModelInfo())
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "params.reasoning_effort").String(); got != "low" {
		t.Fatalf("params.reasoning_effort = %q, want %q, body=%s", got, "low", string(out))
	}
}

func TestApply_ModeNone_UsesFallbackLevelWhenDisableUnsupported(t *testing.T) {
	applier := NewApplier()
	// Validation hands back the lowest supported level for models that cannot
	// disable thinking; the applier must send it instead of clearing the field.
	body := []byte(`{"params":{"model":"minimax/minimax-m3-free","reasoning_effort":"high"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.LevelLow}, commandCodeModelInfo())
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "params.reasoning_effort").String(); got != "low" {
		t.Fatalf("params.reasoning_effort = %q, want %q, body=%s", got, "low", string(out))
	}
}

func TestApply_ModeNone_ClearsEffortWhenNoFallback(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"params":{"model":"minimax/minimax-m3-free","reasoning_effort":"high"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, commandCodeModelInfo())
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if gjson.GetBytes(out, "params.reasoning_effort").Exists() {
		t.Fatalf("params.reasoning_effort should be cleared in ModeNone without fallback level, body=%s", string(out))
	}
}

func TestApply_NoThinkingSupport_Passthrough(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"params":{"model":"m","reasoning_effort":"high"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, &registry.ModelInfo{ID: "m"})
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "params.reasoning_effort").String(); got != "high" {
		t.Fatalf("params.reasoning_effort = %q, want passthrough %q, body=%s", got, "high", string(out))
	}
}

func TestApply_UserDefinedModel_SkipsValidation(t *testing.T) {
	applier := NewApplier()
	body := []byte(`{"params":{"model":"custom-model"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 30000}, nil)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	// Budget 30000 exceeds ThresholdHigh, mapping to xhigh; upstream validates.
	if got := gjson.GetBytes(out, "params.reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("params.reasoning_effort = %q, want %q, body=%s", got, "xhigh", string(out))
	}
}
