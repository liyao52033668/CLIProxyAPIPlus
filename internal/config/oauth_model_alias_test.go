package config

import "testing"

func TestSanitizeOAuthModelAlias_PreservesOptionalFields(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			" CoDeX ": {
				{Name: " gpt-5 ", Alias: " g5 ", Fork: true, DisplayName: " GPT Five "},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) != 2 {
		t.Fatalf("expected 2 sanitized aliases, got %d", len(aliases))
	}
	if aliases[0].Name != "gpt-5" || aliases[0].Alias != "g5" || !aliases[0].Fork || aliases[0].DisplayName != "GPT Five" {
		t.Fatalf("unexpected sanitized first alias: %+v", aliases[0])
	}
	if aliases[1].Name != "gpt-6" || aliases[1].Alias != "g6" || aliases[1].Fork || aliases[1].DisplayName != "" {
		t.Fatalf("unexpected sanitized second alias: %+v", aliases[1])
	}
}

func TestSanitizeOAuthModelAlias_AllowsMultipleAliasesForSameName(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["antigravity"]
	expected := []OAuthModelAlias{
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
	}
	if len(aliases) != len(expected) {
		t.Fatalf("expected %d sanitized aliases, got %d", len(expected), len(aliases))
	}
	for i, exp := range expected {
		if aliases[i].Name != exp.Name || aliases[i].Alias != exp.Alias || aliases[i].Fork != exp.Fork {
			t.Fatalf("expected alias %d to be name=%q alias=%q fork=%v, got name=%q alias=%q fork=%v", i, exp.Name, exp.Alias, exp.Fork, aliases[i].Name, aliases[i].Alias, aliases[i].Fork)
		}
	}
}

// Aliases are unique within a channel: when several upstream names share one
// alias, sanitize keeps only the first. Request routing resolves alias ->
// upstream 1:1, so duplicate aliases have no well-defined target.
func TestSanitizeOAuthModelAlias_DropsDuplicateAliases(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"lobsterai": {
				{Name: "glm-5.2", Alias: "glm"},
				{Name: "glm-5.1", Alias: "glm"},
				{Name: "deepseek-v4-pro", Alias: "ds-pro"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["lobsterai"]
	if len(aliases) != 2 {
		t.Fatalf("expected 2 sanitized aliases, got %d", len(aliases))
	}
	if aliases[0].Name != "glm-5.2" || aliases[0].Alias != "glm" {
		t.Fatalf("expected first duplicate alias to win, got name=%q alias=%q", aliases[0].Name, aliases[0].Alias)
	}
	if aliases[1].Name != "deepseek-v4-pro" || aliases[1].Alias != "ds-pro" {
		t.Fatalf("expected distinct alias kept, got name=%q alias=%q", aliases[1].Name, aliases[1].Alias)
	}
}

// "MiniMax-M3" -> "minimax-m3" differs only by case. Model names compare
// case-insensitively across the whole pipeline (config sanitize, registration
// rename, request routing), so such an alias is an identity mapping and is
// dropped. The rule applies per channel independently: the same entry dies in
// every channel, while genuinely distinct aliases coexist across channels.
func TestSanitizeOAuthModelAlias_CaseOnlyAliasDropped(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"lobsterai": {
				{Name: "deepseek-v4-flash", Alias: "deepseek-v4.1-flash"},
				{Name: "MiniMax-M3", Alias: "minimax-m3"},
			},
			"codebuddy": {
				{Name: "MiniMax-M3", Alias: "minimax-m3"},
			},
			"qoder": {
				{Name: "mmodel", Alias: "minimax-m3"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	lobster := cfg.OAuthModelAlias["lobsterai"]
	if len(lobster) != 1 || lobster[0].Name != "deepseek-v4-flash" {
		t.Fatalf("expected only the non-identity alias to survive, got %+v", lobster)
	}
	if _, exists := cfg.OAuthModelAlias["codebuddy"]; exists {
		t.Fatalf("expected the case-only alias to be dropped in the codebuddy channel too")
	}
	qoder := cfg.OAuthModelAlias["qoder"]
	if len(qoder) != 1 || qoder[0].Name != "mmodel" || qoder[0].Alias != "minimax-m3" {
		t.Fatalf("expected qoder's distinct-name alias to survive, got %+v", qoder)
	}
}

func TestSanitizeOAuthModelAlias_InjectsDefaultKiroAliases(t *testing.T) {
	// When no kiro aliases are configured, defaults should be injected
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	kiroAliases := cfg.OAuthModelAlias["kiro"]
	if len(kiroAliases) == 0 {
		t.Fatal("expected default kiro aliases to be injected")
	}

	expectedAliases := defaultKiroAliases()
	if len(kiroAliases) != len(expectedAliases) {
		t.Fatalf("expected %d default kiro aliases, got %d", len(expectedAliases), len(kiroAliases))
	}

	actualByAlias := make(map[string]OAuthModelAlias, len(kiroAliases))
	for _, a := range kiroAliases {
		actualByAlias[a.Alias] = a
	}
	for _, expected := range expectedAliases {
		actual, ok := actualByAlias[expected.Alias]
		if !ok {
			t.Fatalf("expected default kiro alias %q to be present", expected.Alias)
		}
		if actual.Name != expected.Name || actual.Fork != expected.Fork {
			t.Fatalf(
				"expected default kiro alias %q to be name=%q fork=%v, got name=%q fork=%v",
				expected.Alias,
				expected.Name,
				expected.Fork,
				actual.Name,
				actual.Fork,
			)
		}
	}

	// Codex aliases should still be preserved
	if len(cfg.OAuthModelAlias["codex"]) != 1 {
		t.Fatal("expected codex aliases to be preserved")
	}
}

func TestSanitizeOAuthModelAlias_DoesNotOverrideUserKiroAliases(t *testing.T) {
	// When user has configured kiro aliases, defaults should NOT be injected
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"kiro": {
				{Name: "kiro-claude-sonnet-4", Alias: "my-custom-sonnet", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	kiroAliases := cfg.OAuthModelAlias["kiro"]
	if len(kiroAliases) != 1 {
		t.Fatalf("expected 1 user-configured kiro alias, got %d", len(kiroAliases))
	}
	if kiroAliases[0].Alias != "my-custom-sonnet" {
		t.Fatalf("expected user alias to be preserved, got %q", kiroAliases[0].Alias)
	}
}

func TestSanitizeOAuthModelAlias_DoesNotOverrideUserGitHubCopilotAliases(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"github-copilot": {
				{Name: "claude-opus-4.6", Alias: "my-opus", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	copilotAliases := cfg.OAuthModelAlias["github-copilot"]
	if len(copilotAliases) != 1 {
		t.Fatalf("expected 1 user-configured github-copilot alias, got %d", len(copilotAliases))
	}
	if copilotAliases[0].Alias != "my-opus" {
		t.Fatalf("expected user alias to be preserved, got %q", copilotAliases[0].Alias)
	}
}

func TestSanitizeOAuthModelAlias_DoesNotReinjectAfterExplicitDeletion(t *testing.T) {
	// When user explicitly deletes kiro aliases (key exists with nil value),
	// defaults should NOT be re-injected on subsequent sanitize calls (#222).
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"kiro":  nil, // explicitly deleted
			"codex": {{Name: "gpt-5", Alias: "g5"}},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	kiroAliases := cfg.OAuthModelAlias["kiro"]
	if len(kiroAliases) != 0 {
		t.Fatalf("expected kiro aliases to remain empty after explicit deletion, got %d aliases", len(kiroAliases))
	}
	// The key itself must still be present to prevent re-injection on next reload
	if _, exists := cfg.OAuthModelAlias["kiro"]; !exists {
		t.Fatal("expected kiro key to be preserved as nil marker after sanitization")
	}
	// Other channels should be unaffected
	if len(cfg.OAuthModelAlias["codex"]) != 1 {
		t.Fatal("expected codex aliases to be preserved")
	}
}

func TestSanitizeOAuthModelAlias_GitHubCopilotDoesNotReinjectAfterExplicitDeletion(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"github-copilot": nil, // explicitly deleted
		},
	}

	cfg.SanitizeOAuthModelAlias()

	copilotAliases := cfg.OAuthModelAlias["github-copilot"]
	if len(copilotAliases) != 0 {
		t.Fatalf("expected github-copilot aliases to remain empty after explicit deletion, got %d aliases", len(copilotAliases))
	}
	if _, exists := cfg.OAuthModelAlias["github-copilot"]; !exists {
		t.Fatal("expected github-copilot key to be preserved as nil marker after sanitization")
	}
}

func TestSanitizeOAuthModelAlias_DoesNotReinjectAfterExplicitDeletionEmpty(t *testing.T) {
	// Same as above but with empty slice instead of nil (PUT with empty body).
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"kiro": {}, // explicitly set to empty
		},
	}

	cfg.SanitizeOAuthModelAlias()

	if len(cfg.OAuthModelAlias["kiro"]) != 0 {
		t.Fatalf("expected kiro aliases to remain empty, got %d aliases", len(cfg.OAuthModelAlias["kiro"]))
	}
	if _, exists := cfg.OAuthModelAlias["kiro"]; !exists {
		t.Fatal("expected kiro key to be preserved")
	}
}

func TestSanitizeOAuthModelAlias_InjectsDefaultKiroWhenEmpty(t *testing.T) {
	// When OAuthModelAlias is nil, kiro defaults should still be injected
	cfg := &Config{}

	cfg.SanitizeOAuthModelAlias()

	kiroAliases := cfg.OAuthModelAlias["kiro"]
	if len(kiroAliases) == 0 {
		t.Fatal("expected default kiro aliases to be injected when OAuthModelAlias is nil")
	}
}

// func TestSanitizeOAuthModelAlias_InjectsDefaultQoderWhenEmpty(t *testing.T) {
// 	// When OAuthModelAlias is nil, qoder defaults should still be injected
// 	cfg := &Config{}

// 	cfg.SanitizeOAuthModelAlias()

// 	qoderAliases := cfg.OAuthModelAlias["qoder"]
// 	if len(qoderAliases) == 0 {
// 		t.Fatal("expected default qoder aliases to be injected when OAuthModelAlias is nil")
// 	}
// }
