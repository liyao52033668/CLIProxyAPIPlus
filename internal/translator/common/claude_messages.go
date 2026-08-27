package common

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// AlignClaudeToolResults orders tool_result blocks by the preceding tool_use IDs.
// Other content blocks retain their relative order after the tool results. If a
// complete one-to-one match is unavailable, the original content is returned.
func AlignClaudeToolResults(content gjson.Result, toolUseIDs []string) gjson.Result {
	if !content.IsArray() || len(toolUseIDs) == 0 {
		return content
	}

	parts := content.Array()
	toolResults := make([]gjson.Result, 0, len(toolUseIDs))
	otherParts := make([]gjson.Result, 0, len(parts))
	for _, part := range parts {
		if part.Get("type").String() == "tool_result" {
			toolResults = append(toolResults, part)
			continue
		}
		otherParts = append(otherParts, part)
	}
	if len(toolResults) != len(toolUseIDs) {
		return content
	}

	ordered := make([][]byte, 0, len(parts))
	used := make([]bool, len(toolResults))
	for _, toolUseID := range toolUseIDs {
		matched := -1
		for resultIndex, toolResult := range toolResults {
			if !used[resultIndex] && toolUseID != "" && toolResult.Get("tool_use_id").String() == toolUseID {
				matched = resultIndex
				break
			}
		}
		if matched < 0 {
			return content
		}
		used[matched] = true
		ordered = append(ordered, []byte(toolResults[matched].Raw))
	}
	for _, part := range otherParts {
		ordered = append(ordered, []byte(part.Raw))
	}
	return gjson.ParseBytes(util.JoinRawArrayBytes(ordered))
}
