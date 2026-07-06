package thinking

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestExtractReasoningEffortUsesSuffixOverBody(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"reasoning_effort":"low"}`), "openai", "gpt-5.4(high)")
	if got != "high" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "high")
	}
}

func TestExtractReasoningEffortConvertsBudgetToLevel(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`), "claude", "claude-sonnet-4-5")
	if got != "medium" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "medium")
	}
}

func TestExtractReasoningEffortSupportsOpenAIResponses(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"reasoning":{"effort":"medium"}}`), "openai-response", "gpt-5.4")
	if got != "medium" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "medium")
	}
}

func TestExtractReasoningEffortMissingConfigIsEmpty(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "openai", "gpt-5.4")
	if got != "" {
		t.Fatalf("ExtractReasoningEffort() = %q, want empty", got)
	}
}

func TestClampBudgetLevelOnlyRangeDoesNotWarnForZero(t *testing.T) {
	hook := logrustest.NewGlobal()
	defer hook.Reset()

	modelInfo := &registry.ModelInfo{
		ID: "gpt-5.5",
		Thinking: &registry.ThinkingSupport{
			Min:    0,
			Max:    0,
			Levels: []string{"low", "medium", "high"},
		},
	}

	got := clampBudget(0, modelInfo, "codex")
	if got != 0 {
		t.Fatalf("clampBudget() = %d, want 0", got)
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "budget zero not allowed") {
			t.Fatalf("unexpected warning: %s", entry.Message)
		}
	}
}
