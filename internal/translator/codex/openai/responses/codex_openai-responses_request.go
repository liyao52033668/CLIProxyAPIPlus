package responses

import (
	"bytes"
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON

	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON = setCodexRequiredInclude(rawJSON)
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() {
		if v.Type == gjson.String {
			switch strings.ToLower(strings.TrimSpace(v.String())) {
			case "priority", "fast":
				if v.String() != "priority" {
					rawJSON, _ = sjson.SetBytes(rawJSON, "service_tier", "priority")
				}
			case "ultrafast":
				if v.String() != "ultrafast" {
					rawJSON, _ = sjson.SetBytes(rawJSON, "service_tier", "ultrafast")
				}
			default:
				rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
			}
		} else {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)
	rawJSON = normalizeEmptyFunctionCallArguments(rawJSON)
	rawJSON = stripCodexResponsesCacheBreakpoints(rawJSON)

	return rawJSON
}

// setCodexRequiredInclude normalizes the Codex "include" field while preserving
// client-requested web search sources. It avoids payload re-encoding when the
// include array is already in its normalized form.
func setCodexRequiredInclude(rawJSON []byte) []byte {
	current := gjson.GetBytes(rawJSON, "include")
	includeSources := false
	if current.IsArray() {
		values := current.Array()
		for _, value := range values {
			if value.Type == gjson.String && value.String() == "web_search_call.action.sources" {
				includeSources = true
				break
			}
		}
		if !includeSources && len(values) == 1 && values[0].Type == gjson.String && values[0].String() == "reasoning.encrypted_content" {
			return rawJSON
		}
		if includeSources && len(values) == 2 &&
			values[0].Type == gjson.String && values[0].String() == "reasoning.encrypted_content" &&
			values[1].Type == gjson.String && values[1].String() == "web_search_call.action.sources" {
			return rawJSON
		}
	}

	encoded := []byte(`["reasoning.encrypted_content"]`)
	if includeSources {
		encoded = []byte(`["reasoning.encrypted_content","web_search_call.action.sources"]`)
	}
	updated, errSet := sjson.SetRawBytes(rawJSON, "include", encoded)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// normalizeEmptyFunctionCallArguments rewrites blank string arguments on
// history function_call items to "{}". Some Responses clients serialize
// parameter-less tool calls as an empty string, which strict Codex Responses
// upstreams reject with "`arguments` must be valid JSON". Only blank strings
// are touched: non-empty strings (even invalid JSON) pass through unchanged
// so other errors keep their original shape, and custom_tool_call items use
// the separate input field and are never affected.
func normalizeEmptyFunctionCallArguments(rawJSON []byte) []byte {
	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}
	items := inputResult.Array()
	if len(items) == 0 {
		return rawJSON
	}
	changed := false
	rebuilt := make([][]byte, 0, len(items))
	for _, item := range items {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("type").String() == "function_call" {
			if args := item.Get("arguments"); args.Type == gjson.String && strings.TrimSpace(args.String()) == "" {
				if updated, errSet := sjson.SetBytes(itemRaw, "arguments", "{}"); errSet == nil {
					itemRaw = updated
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, itemRaw)
	}
	if !changed {
		return rawJSON
	}
	updated, errSetRaw := sjson.SetRawBytes(rawJSON, "input", translatorcommon.JoinRawArray(rebuilt))
	if errSetRaw != nil {
		return rawJSON
	}
	return updated
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	// Directly modify role values for items with "system" role
	for i := range inputArray {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := range toolArray {
			typePath := fmt.Sprintf("tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")

	toolChoiceTools := gjson.GetBytes(result, "tool_choice.tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := range toolArray {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath)
		}
	}

	return result
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

// stripCodexResponsesCacheBreakpoints removes any "prompt_cache_breakpoint" hint
// attached to input items: inside content-part arrays (message input[].content[]
// and function_call_output input[].output[]) or as an item-level field. Some
// clients (e.g. GitHub Copilot CLI) attach this field per content item when
// targeting the OpenAI Responses format. Codex Responses rejects it outright:
// {"error":{"message":"prompt_cache_breakpoint is not supported on this model", ...}}.
func stripCodexResponsesCacheBreakpoints(rawJSON []byte) []byte {
	if !bytes.Contains(rawJSON, []byte(`"prompt_cache_breakpoint"`)) {
		return rawJSON
	}

	input := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if !input.IsArray() {
		return rawJSON
	}

	inputItems := input.Array()
	if len(inputItems) == 0 {
		return rawJSON
	}

	changed := false
	rebuiltInput := make([][]byte, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		for _, arrayPath := range []string{"content", "output"} {
			arrayResult := item.Get(arrayPath)
			if !arrayResult.IsArray() {
				continue
			}
			updatedArray, arrayChanged := stripPromptCacheBreakpointFromContent(arrayResult)
			if !arrayChanged {
				continue
			}
			if updatedItem, errSet := sjson.SetRawBytes(itemRaw, arrayPath, updatedArray); errSet == nil {
				itemRaw = updatedItem
				changed = true
			}
		}
		if item.Get("prompt_cache_breakpoint").Exists() {
			if updatedItem, errDelete := sjson.DeleteBytes(itemRaw, "prompt_cache_breakpoint"); errDelete == nil {
				itemRaw = updatedItem
				changed = true
			}
		}
		rebuiltInput = append(rebuiltInput, itemRaw)
	}
	if !changed {
		return rawJSON
	}

	updated, errSet := sjson.SetRawBytes(rawJSON, "input", translatorcommon.JoinRawArray(rebuiltInput))
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// stripPromptCacheBreakpointFromContent removes "prompt_cache_breakpoint" from each
// content part that carries it and reports whether anything changed.
func stripPromptCacheBreakpointFromContent(content gjson.Result) ([]byte, bool) {
	parts := content.Array()
	hasBreakpoint := false
	for _, part := range parts {
		if part.Get("prompt_cache_breakpoint").Exists() {
			hasBreakpoint = true
			break
		}
	}
	if !hasBreakpoint {
		return nil, false
	}

	changed := false
	rebuiltParts := make([][]byte, 0, len(parts))
	for _, part := range parts {
		partRaw := []byte(part.Raw)
		if part.Get("prompt_cache_breakpoint").Exists() {
			if updated, errDelete := sjson.DeleteBytes(partRaw, "prompt_cache_breakpoint"); errDelete == nil {
				partRaw = updated
				changed = true
			}
		}
		rebuiltParts = append(rebuiltParts, partRaw)
	}
	if !changed {
		return nil, false
	}
	return translatorcommon.JoinRawArray(rebuiltParts), true
}
