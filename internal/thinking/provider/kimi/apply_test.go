package kimi

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApply_ModeNone_UsesDisabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "kimi-k2.5",
		Thinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "high"}},
	}
	body := []byte(`{"model":"kimi-k2.5","reasoning_effort":"none","thinking":{"type":"enabled","effort":"high"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "disabled", string(out))
	}
	if gjson.GetBytes(out, "thinking.effort").Exists() {
		t.Fatalf("thinking.effort should be removed, body=%s", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed in ModeNone, body=%s", string(out))
	}
}

func TestApply_ModeLevel_UsesNativeThinkingObject(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "kimi-k2.5",
		Thinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "high"}},
	}
	body := []byte(`{"model":"kimi-k2.5","reasoning_effort":"low","thinking":{"type":"disabled","keep":"all"}}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "enabled", string(out))
	}
	if got := gjson.GetBytes(out, "thinking.effort").String(); got != "high" {
		t.Fatalf("thinking.effort = %q, want %q, body=%s", got, "high", string(out))
	}
	if got := gjson.GetBytes(out, "thinking.keep").String(); got != "all" {
		t.Fatalf("thinking.keep = %q, want %q, body=%s", got, "all", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed when native thinking is used, body=%s", string(out))
	}
}

func TestApply_UserDefinedModeNone_UsesDisabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:          "custom-kimi-model",
		UserDefined: true,
	}
	body := []byte(`{"model":"custom-kimi-model","reasoning_effort":"none"}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "disabled", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed in ModeNone, body=%s", string(out))
	}
}

func TestApply_ModeNone_WithFallbackLevel_UsesEnabledThinking(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:       "kimi-k2.7-code",
		Thinking: &registry.ThinkingSupport{ZeroAllowed: false, Levels: []string{"low", "high"}},
	}
	body := []byte(`{"model":"kimi-k2.7-code","reasoning_effort":"none"}`)

	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.LevelLow}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "enabled", string(out))
	}
	if got := gjson.GetBytes(out, "thinking.effort").String(); got != "low" {
		t.Fatalf("thinking.effort = %q, want %q, body=%s", got, "low", string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed, body=%s", string(out))
	}
}
