package config

import "strings"

// defaultKiroAliases returns default oauth-model-alias entries for Kiro.
// These aliases expose standard Claude IDs for Kiro-prefixed upstream models.
func defaultKiroAliases() []OAuthModelAlias {
	return []OAuthModelAlias{
		// Sonnet 4.6
		{Name: "kiro-claude-sonnet-4-6", Alias: "claude-sonnet-4-6", Fork: false},
		// Sonnet 4.5
		{Name: "kiro-claude-sonnet-4-5", Alias: "claude-sonnet-4-5", Fork: false},
		// Sonnet 4
		{Name: "kiro-claude-sonnet-4", Alias: "claude-sonnet-4", Fork: false},
		// Opus 4.6
		{Name: "kiro-claude-opus-4-6", Alias: "claude-opus-4-6", Fork: false},
		// Opus 4.5
		{Name: "kiro-claude-opus-4-5", Alias: "claude-opus-4-5", Fork: false},
		// Haiku 4.5
		{Name: "kiro-claude-haiku-4-5", Alias: "claude-haiku-4-5", Fork: false},
	}
}

// QoderAliasesFromModels generates oauth-model-alias entries from a dynamic
// list of Qoder models. Each alias is the lower-cased upstream DisplayName
// (e.g. "Qwen3.8-Max" → "qwen3.8-max"), so newly released upstream models
// become addressable without code or config changes. Entries whose alias is
// case-identical to the model ID are skipped (identity mapping).
func QoderAliasesFromModels(models []QoderModelRef) []OAuthModelAlias {
	var aliases []OAuthModelAlias
	seen := make(map[string]struct{})
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		displayName := strings.TrimSpace(model.DisplayName)
		if id == "" || displayName == "" {
			continue
		}
		alias := strings.ToLower(displayName)
		if strings.EqualFold(alias, id) {
			continue
		}
		key := id + "→" + alias
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		aliases = append(aliases, OAuthModelAlias{Name: id, Alias: alias})
	}
	return aliases
}

// QoderModelRef is the minimal model metadata needed to derive Qoder aliases.
type QoderModelRef struct {
	ID          string
	DisplayName string
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
