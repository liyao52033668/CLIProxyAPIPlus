// Package thinking provides shared helpers for Claude-format thinking extraction.
package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeOutputEffort reads output_config.effort from a Claude-format request body.
// Returns the effort string (lowercased, trimmed) or empty if absent.
//
// Claude 4.6 adaptive thinking uses output_config.effort (low/medium/high/max).
// This helper is used by translators to honor the client's explicit effort level
// regardless of whether thinking.type is "enabled" or "adaptive".
func ClaudeOutputEffort(body []byte) string {
	if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		return strings.ToLower(strings.TrimSpace(effort.String()))
	}
	return ""
}
