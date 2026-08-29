package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveCursorModelIDPassthrough(t *testing.T) {
	cases := []struct {
		name     string
		request  string
		catalog  []string
		expected string
	}{
		{"empty request", "", []string{"sonnet-4.6"}, ""},
		{"default passes through", "default", []string{"sonnet-4.6"}, "default"},
		{"auto passes through", "Auto", []string{"sonnet-4.6"}, "Auto"},
		{"empty catalog", "claude-sonnet-4-5", nil, "claude-sonnet-4-5"},
		{"exact hit keeps request", "Sonnet-4.6", []string{"sonnet-4.6"}, "Sonnet-4.6"},
		{"non-claude name untouched", "gpt-4o", []string{"gpt-4o", "sonnet-4.6"}, "gpt-4o"},
		{"unknown family untouched", "deepseek-v4", []string{"sonnet-4.6"}, "deepseek-v4"},
		{"no family candidates untouched", "claude-9-opus", []string{"sonnet-4.6"}, "claude-9-opus"},
	}
	for _, tc := range cases {
		if got := ResolveCursorModelID(tc.request, tc.catalog); got != tc.expected {
			t.Errorf("%s: ResolveCursorModelID(%q) = %q, want %q", tc.name, tc.request, got, tc.expected)
		}
	}
}

func TestResolveCursorModelIDNormalizedHit(t *testing.T) {
	catalog := []string{"sonnet-4.5", "sonnet-4.6"}
	if got := ResolveCursorModelID("SONNET-4.6-20260115", catalog); got != "sonnet-4.6" {
		t.Errorf("dated snapshot: got %q, want sonnet-4.6", got)
	}
	if got := ResolveCursorModelID("sonnet-4.5-v2", catalog); got != "sonnet-4.5" {
		t.Errorf("-vN suffix: got %q, want sonnet-4.5", got)
	}
}

func TestResolveCursorModelIDMapping(t *testing.T) {
	catalog := []string{"composer-2", "sonnet-4.5", "sonnet-4.6", "sonnet-4.6-thinking", "opus-4.6"}
	cases := []struct {
		name     string
		request  string
		expected string
	}{
		{"dated anthropic snapshot", "claude-sonnet-4-5-20250929", "sonnet-4.5"},
		{"dotted anthropic name", "claude-sonnet-4.6", "sonnet-4.6"},
		{"haiku substitutes sonnet", "claude-haiku-4-5-20251001", "sonnet-4.5"},
		{"major-only picks newest minor", "claude-sonnet-4", "sonnet-4.6"},
		{"thinking variant preferred", "claude-sonnet-4-6-thinking", "sonnet-4.6-thinking"},
		{"thinking falls back to base", "claude-opus-4-6-thinking", "opus-4.6"},
		{"missing minor upgrades to newest", "claude-sonnet-4-7", "sonnet-4.6"},
		{"cursor-style id normalizes", "claude-4-sonnet", "sonnet-4.6"},
		{"non-thinking keeps base", "claude-opus-4-6", "opus-4.6"},
	}
	for _, tc := range cases {
		if got := ResolveCursorModelID(tc.request, catalog); got != tc.expected {
			t.Errorf("%s: ResolveCursorModelID(%q) = %q, want %q", tc.name, tc.request, got, tc.expected)
		}
	}
}

func TestResolveCursorModelIDClaudeStyleCatalog(t *testing.T) {
	// Some catalog generations use claude-prefixed IDs.
	catalog := []string{"claude-4-sonnet", "claude-3.5-sonnet", "claude-4-opus"}
	if got := ResolveCursorModelID("claude-sonnet-4-5-20250929", catalog); got != "claude-4-sonnet" {
		t.Errorf("claude-style catalog: got %q, want claude-4-sonnet", got)
	}
	if got := ResolveCursorModelID("claude-3-5-sonnet-20241022", catalog); got != "claude-3.5-sonnet" {
		t.Errorf("claude-3.5 request: got %q, want claude-3.5-sonnet", got)
	}
}

func TestExpandCursorModelAliases(t *testing.T) {
	in := []*registry.ModelInfo{
		{ID: "composer-2", OwnedBy: "cursor"},
		{ID: "sonnet-4.6", OwnedBy: "cursor", DisplayName: "Sonnet 4.6"},
		{ID: "sonnet-4.6-thinking", OwnedBy: "cursor", DisplayName: "Sonnet 4.6 Thinking"},
		{ID: "claude-4-sonnet", OwnedBy: "cursor"},
		{ID: "claude-sonnet-4-6", OwnedBy: "cursor"},
	}
	out := ExpandCursorModelAliases(in)
	ids := make(map[string]bool, len(out))
	for _, m := range out {
		ids[m.ID] = true
	}
	for _, want := range []string{"sonnet-4.6", "claude-sonnet-4-6", "claude-sonnet-4-6-thinking", "claude-4-sonnet"} {
		if !ids[want] {
			t.Errorf("expected alias/list entry %q in output", want)
		}
	}
	if ids["claude-sonnet-4-5"] {
		t.Errorf("alias for absent target sonnet-4.5 must not be advertised")
	}
	if ids["claude-composer-2"] {
		t.Errorf("non-claude models must not get aliases")
	}
	// The alias entry inherits the target's metadata but keeps its own ID.
	for _, m := range out {
		if m.ID == "claude-sonnet-4-6-thinking" {
			if m.DisplayName != "Sonnet 4.6 Thinking" {
				t.Errorf("alias DisplayName = %q, want target's %q", m.DisplayName, "Sonnet 4.6 Thinking")
			}
			if m.OwnedBy != "cursor" {
				t.Errorf("alias OwnedBy = %q, want cursor", m.OwnedBy)
			}
		}
	}
}

func TestExpandCursorModelAliasesEmpty(t *testing.T) {
	if out := ExpandCursorModelAliases(nil); len(out) != 0 {
		t.Errorf("empty input must stay empty, got %d entries", len(out))
	}
}
