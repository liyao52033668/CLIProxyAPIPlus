package cache

import (
	"strings"
	"testing"
)

func TestOpenAICompatReasoningReplayRoundTrip(t *testing.T) {
	clearOpenAICompatReasoningReplayCacheForTest(t)

	StoreOpenAICompatReasoningTurn("caller-a", "step one think", []string{"call_0_abc", "call_0_def"}, "fingerprint-1")

	if reasoning, ok := GetOpenAICompatReasoningForToolCall("caller-a", "call_0_abc"); !ok || reasoning != "step one think" {
		t.Fatalf("tool call hit = %q, %v, want step one think", reasoning, ok)
	}
	if reasoning, ok := GetOpenAICompatReasoningForToolCall("caller-a", "call_0_def"); !ok || reasoning != "step one think" {
		t.Fatalf("second tool call hit = %q, %v, want step one think", reasoning, ok)
	}
	if reasoning, ok := GetOpenAICompatReasoningForContent("caller-a", "fingerprint-1"); !ok || reasoning != "step one think" {
		t.Fatalf("content hit = %q, %v, want step one think", reasoning, ok)
	}
	if reasoning, ok := GetOpenAICompatLatestReasoning("caller-a"); !ok || reasoning != "step one think" {
		t.Fatalf("latest hit = %q, %v, want step one think", reasoning, ok)
	}
}

func TestOpenAICompatReasoningReplayCallerIsolation(t *testing.T) {
	clearOpenAICompatReasoningReplayCacheForTest(t)

	StoreOpenAICompatReasoningTurn("caller-a", "reasoning a", []string{"call_a"}, "fp-a")
	StoreOpenAICompatReasoningTurn("caller-b", "reasoning b", []string{"call_b"}, "fp-b")

	if _, ok := GetOpenAICompatReasoningForToolCall("caller-b", "call_a"); ok {
		t.Fatal("caller-b must not see caller-a tool call entries")
	}
	if _, ok := GetOpenAICompatReasoningForContent("caller-a", "fp-b"); ok {
		t.Fatal("caller-a must not see caller-b content entries")
	}
	if reasoning, ok := GetOpenAICompatLatestReasoning("caller-b"); !ok || reasoning != "reasoning b" {
		t.Fatalf("caller-b latest = %q, %v, want reasoning b", reasoning, ok)
	}
}

func TestOpenAICompatReasoningReplayEmptyCallerUsesDefaultScope(t *testing.T) {
	clearOpenAICompatReasoningReplayCacheForTest(t)

	StoreOpenAICompatReasoningTurn("", "keyless reasoning", []string{"call_k"}, "")

	if reasoning, ok := GetOpenAICompatReasoningForToolCall("", "call_k"); !ok || reasoning != "keyless reasoning" {
		t.Fatalf("keyless tool call hit = %q, %v, want keyless reasoning", reasoning, ok)
	}
	if reasoning, ok := GetOpenAICompatLatestReasoning(" "); !ok || reasoning != "keyless reasoning" {
		t.Fatalf("blank caller latest = %q, %v, want keyless reasoning", reasoning, ok)
	}
}

func TestOpenAICompatReasoningReplayRejectsEmptyReasoning(t *testing.T) {
	clearOpenAICompatReasoningReplayCacheForTest(t)

	StoreOpenAICompatReasoningTurn("caller-a", "   ", []string{"call_x"}, "fp-x")
	StoreOpenAICompatReasoningTurn("caller-a", strings.Repeat("r", OpenAICompatReasoningReplayCacheMaxBytesPerEntry+1), []string{"call_big"}, "fp-big")

	if _, ok := GetOpenAICompatLatestReasoning("caller-a"); ok {
		t.Fatal("empty reasoning must not be cached")
	}
	if _, ok := GetOpenAICompatReasoningForToolCall("caller-a", "call_big"); ok {
		t.Fatal("oversized reasoning must not be cached")
	}
}

func TestOpenAICompatReasoningReplayLatestOverwrite(t *testing.T) {
	clearOpenAICompatReasoningReplayCacheForTest(t)

	StoreOpenAICompatReasoningTurn("caller-a", "first", nil, "")
	StoreOpenAICompatReasoningTurn("caller-a", "second", nil, "")

	if reasoning, ok := GetOpenAICompatLatestReasoning("caller-a"); !ok || reasoning != "second" {
		t.Fatalf("latest = %q, %v, want second", reasoning, ok)
	}
}

func clearOpenAICompatReasoningReplayCacheForTest(t *testing.T) {
	t.Helper()
	openAICompatReasoningReplayMu.Lock()
	openAICompatReasoningReplayEntries = make(map[string]openAICompatReasoningReplayEntry)
	openAICompatReasoningReplayMu.Unlock()
	t.Cleanup(func() {
		openAICompatReasoningReplayMu.Lock()
		openAICompatReasoningReplayEntries = make(map[string]openAICompatReasoningReplayEntry)
		openAICompatReasoningReplayMu.Unlock()
	})
}
