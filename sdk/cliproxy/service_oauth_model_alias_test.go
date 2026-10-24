package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", DisplayName: "Configured GPT Five"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
	if out[0].DisplayName != "Configured GPT Five" {
		t.Fatalf("expected display name %q, got %q", "Configured GPT Five", out[0].DisplayName)
	}
}

func TestApplyOAuthModelAlias_XAIRename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"xai": {
				{Name: "grok-4.3", Alias: "grok-latest"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "grok-4.3", Name: "models/grok-4.3", DisplayName: "Grok 4.3"},
	}

	out := applyOAuthModelAlias(cfg, "xai", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "grok-latest" {
		t.Fatalf("expected model id %q, got %q", "grok-latest", out[0].ID)
	}
}

// Ported OAuth providers must rename their catalog models too; a missing entry
// in OAuthModelAliasChannel makes applyOAuthModelAlias a silent no-op.
func TestApplyOAuthModelAlias_PortedProvidersRename(t *testing.T) {
	cases := []struct {
		provider string
		name     string
		alias    string
	}{
		{provider: "lobsterai", name: "deepseek-v4-pro", alias: "ds-pro"},
		{provider: "devin", name: "swe-1-7", alias: "devin-swe"},
		{provider: "freebuff", name: "claude-sonnet-4-5", alias: "fb-sonnet"},
		{provider: "alysis", name: "deepseek-v4-flash", alias: "ds-flash"},
	}

	for _, tt := range cases {
		t.Run(tt.provider, func(t *testing.T) {
			cfg := &config.Config{
				OAuthModelAlias: map[string][]config.OAuthModelAlias{
					tt.provider: {{Name: tt.name, Alias: tt.alias}},
				},
			}
			models := []*ModelInfo{{ID: tt.name, Name: "models/" + tt.name}}

			out := applyOAuthModelAlias(cfg, tt.provider, "oauth", models)
			if len(out) != 1 {
				t.Fatalf("expected 1 model, got %d", len(out))
			}
			if out[0].ID != tt.alias {
				t.Fatalf("expected model id %q, got %q", tt.alias, out[0].ID)
			}
		})
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true, DisplayName: "Configured GPT Five"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[0].DisplayName != "Upstream GPT Five" {
		t.Fatalf("expected original display name %q, got %q", "Upstream GPT Five", out[0].DisplayName)
	}
	if out[1].DisplayName != "Configured GPT Five" {
		t.Fatalf("expected alias display name %q, got %q", "Configured GPT Five", out[1].DisplayName)
	}
}

func TestApplyOAuthModelAlias_PreservesUpstreamDisplayNameByDefault(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].DisplayName != "Upstream GPT Five" {
		t.Fatalf("expected upstream display name %q, got %q", "Upstream GPT Five", out[0].DisplayName)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
}

func TestApplyOAuthModelAlias_GitHubCopilotGeneratesAliasesFromDynamicModels(t *testing.T) {
	cfg := &config.Config{}
	cfg.SanitizeOAuthModelAlias()

	models := []*ModelInfo{
		{ID: "claude-opus-4.6", Name: "models/claude-opus-4.6"},
	}

	out := applyOAuthModelAlias(cfg, "github-copilot", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected original and generated alias model, got %d", len(out))
	}
	if out[0].ID != "claude-opus-4.6" {
		t.Fatalf("expected first model id %q, got %q", "claude-opus-4.6", out[0].ID)
	}
	if out[1].ID != "claude-opus-4-6" {
		t.Fatalf("expected generated alias model id %q, got %q", "claude-opus-4-6", out[1].ID)
	}
	if out[1].Name != "models/claude-opus-4-6" {
		t.Fatalf("expected generated alias model name %q, got %q", "models/claude-opus-4-6", out[1].Name)
	}
}

// Qoder aliases are auto-derived from the fetched catalog's DisplayNames and
// replace the client-visible model ID; MetadataModelID carries the upstream ID
// so request-time resolution can map the alias back.
func TestApplyOAuthModelAlias_QoderGeneratesAliasesFromDisplayNames(t *testing.T) {
	cfg := &config.Config{}

	models := []*ModelInfo{
		{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"},
		{ID: "auto", DisplayName: "Auto"},
	}

	out := applyOAuthModelAlias(cfg, "qoder", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models (identity 'auto' + alias), got %d", len(out))
	}
	byID := make(map[string]*ModelInfo, len(out))
	for _, model := range out {
		byID[strings.ToLower(model.ID)] = model
	}
	alias, ok := byID["qwen3.8-max"]
	if !ok {
		t.Fatalf("expected auto-generated alias id %q, got ids %v", "qwen3.8-max", len(byID))
	}
	if alias.MetadataModelID != "qmodel_38max" {
		t.Fatalf("expected MetadataModelID %q, got %q", "qmodel_38max", alias.MetadataModelID)
	}
	if alias.DisplayName != "Qwen3.8-Max" {
		t.Fatalf("expected display name %q, got %q", "Qwen3.8-Max", alias.DisplayName)
	}
	// "Auto" lower-cases to the ID itself: identity mapping, no rename.
	if _, ok := byID["auto"]; !ok {
		t.Fatal("expected identity model 'auto' to keep its ID")
	}
}

// User-configured qoder aliases win over auto-derived ones on alias conflicts.
func TestApplyOAuthModelAlias_QoderUserAliasOverridesAuto(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"qoder": {{Name: "qmodel_38max", Alias: "my-max"}},
		},
	}

	models := []*ModelInfo{
		{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"},
	}

	out := applyOAuthModelAlias(cfg, "qoder", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "my-max" {
		t.Fatalf("expected user alias id %q, got %q", "my-max", out[0].ID)
	}
	if out[0].MetadataModelID != "qmodel_38max" {
		t.Fatalf("expected MetadataModelID %q, got %q", "qmodel_38max", out[0].MetadataModelID)
	}
}

// An explicitly emptied qoder channel is a "disabled" marker: no auto aliases.
func TestApplyOAuthModelAlias_QoderExplicitEmptyDisablesAutoAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"qoder": nil,
		},
	}

	models := []*ModelInfo{
		{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"},
	}

	out := applyOAuthModelAlias(cfg, "qoder", "oauth", models)
	if len(out) != 1 || out[0].ID != "qmodel_38max" {
		t.Fatalf("expected upstream model without aliasing, got %+v", out)
	}
}
