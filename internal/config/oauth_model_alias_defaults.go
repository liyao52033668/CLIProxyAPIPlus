package config

import "strings"

// defaultKiroAliases returns default oauth-model-alias entries for Kiro.
// These aliases expose standard Claude IDs for Kiro-prefixed upstream models.
func defaultKiroAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		// Sonnet 4.6
		{Name: "kiro-claude-sonnet-4-6", Alias: "claude-sonnet-4-6", Fork: true},
		// Sonnet 4.5
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5-20250929", Fork: true},
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5", Fork: true},
		// Sonnet 4
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4-20250514", Fork: true},
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4", Fork: true},
		// Opus 4.6
		{Name: "kiro-claude-opus-4-6", Alias: "claude-opus-4-6", Fork: true},
		// Opus 4.5
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5", Fork: true},
		// Haiku 4.5
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5-20251001", Fork: true},
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5", Fork: true},
	}
}

func defaultQoderAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		{Name: "qmodel_latest", Alias: "qwen3.7-max"},
		{Name: "qmodel", Alias: "qwen3.7-plus"},
		{Name: "dmodel", Alias: "deepseek-v4-pro"},
		{Name: "dfmodel", Alias: "deepseek-v4-flash"},
		{Name: "gm51model", Alias: "glm-5.2"},
		{Name: "kmodel", Alias: "kimi-k2.7"},
		{Name: "mmodel", Alias: "minimax-m3"},
	}
}

// GitHubCopilotAliasesFromModels generates oauth-model-alias entries from a dynamic
// list of model IDs fetched from the Copilot API. It auto-creates aliases for
// models whose ID contains a dot (e.g. "claude-opus-4.6" → "claude-opus-4-6"),
// which is the pattern used by Claude models on Copilot.
func GitHubCopilotAliasesFromModels(modelIDs []string) []OAuthModelAlias {
	var aliases []OAuthModelAlias
	seen := make(map[string]struct{})
	for _, id := range modelIDs {
		if !strings.Contains(id, ".") {
			continue
		}
		hyphenID := strings.ReplaceAll(id, ".", "-")
		key := id + "→" + hyphenID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		aliases = append(aliases, OAuthModelAlias{Name: id, Alias: hyphenID, Fork: true})
	}
	return aliases
}
