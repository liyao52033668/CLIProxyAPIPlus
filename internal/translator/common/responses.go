package common

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SetResponsesToolCallIdentity writes a resolved Responses tool name and namespace.
func SetResponsesToolCallIdentity(item []byte, name, namespace, itemPath string) []byte {
	namePath := "name"
	namespacePath := "namespace"
	if itemPath != "" {
		namePath = itemPath + ".name"
		namespacePath = itemPath + ".namespace"
	}
	item, _ = sjson.SetBytes(item, namePath, name)
	if namespace != "" {
		item, _ = sjson.SetBytes(item, namespacePath, namespace)
	} else {
		item, _ = sjson.DeleteBytes(item, namespacePath)
	}
	return item
}

// ExtractResponsesCallID extracts the tool call ID from a Responses API item,
// prioritizing dedicated tool call references over generic item IDs:
// call_id -> tool_call_id -> callId -> id (excluding fco_ output item IDs)
func ExtractResponsesCallID(node gjson.Result) string {
	if callID := strings.TrimSpace(node.Get("call_id").String()); callID != "" {
		return callID
	}
	if toolCallID := strings.TrimSpace(node.Get("tool_call_id").String()); toolCallID != "" {
		return toolCallID
	}
	if callId := strings.TrimSpace(node.Get("callId").String()); callId != "" {
		return callId
	}
	id := strings.TrimSpace(node.Get("id").String())
	if strings.HasPrefix(id, "fco_") {
		return ""
	}
	return id
}

// NormalizeResponsesToolCallOutputs scans a slice of Responses input items,
// pairs function_call_output / custom_tool_call_output with preceding pending tool calls,
// and assigns missing call_ids to tool outputs using a multi-pass matching strategy:
// 1. Exact explicit call ID match (reserving calls for outputs that explicitly reference them across the entire conversation).
// 2. Function name match for outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// 3. FIFO queue fallback for remaining outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// Outputs that already carry an explicit call ID that does not match any pending call
// are never rewritten or stolen.
func NormalizeResponsesToolCallOutputs(items []gjson.Result) []gjson.Result {
	if len(items) == 0 {
		return items
	}

	normalized := make([]gjson.Result, len(items))
	copy(normalized, items)

	explicitOutputCounts := make(map[string]int)
	for _, item := range items {
		typ := item.Get("type").String()
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			if id := ExtractResponsesCallID(item); id != "" {
				explicitOutputCounts[id]++
			}
		}
	}

	var pendingCallIDs []string
	pendingCallNames := make(map[string]string)

	i := 0
	for i < len(normalized) {
		item := normalized[i]
		itemType := item.Get("type").String()

		switch itemType {
		case "function_call", "custom_tool_call":
			callID := ExtractResponsesCallID(item)
			if callID != "" {
				pendingCallIDs = append(pendingCallIDs, callID)
				name := item.Get("name").String()
				pendingCallNames[callID] = name
			}
			i++

		case "function_call_output", "custom_tool_call_output":
			start := i
			for i < len(normalized) && (normalized[i].Get("type").String() == "function_call_output" || normalized[i].Get("type").String() == "custom_tool_call_output") {
				i++
			}
			outputs := normalized[start:i]

			if len(pendingCallIDs) > 0 {
				used := make([]bool, len(outputs))
				matchedForPending := make([]int, len(pendingCallIDs))
				for idx := range matchedForPending {
					matchedForPending[idx] = -1
				}

				// Pass 1: exact explicit call ID match
				for pendingIdx, pendingID := range pendingCallIDs {
					for outIdx, out := range outputs {
						if !used[outIdx] && ExtractResponsesCallID(out) == pendingID {
							used[outIdx] = true
							matchedForPending[pendingIdx] = outIdx
							explicitOutputCounts[pendingID]--
							break
						}
					}
				}

				// Pass 2: match by function name for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx, pendingID := range pendingCallIDs {
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					expectedName := pendingCallNames[pendingID]
					if expectedName != "" {
						for outIdx, out := range outputs {
							if !used[outIdx] && ExtractResponsesCallID(out) == "" {
								outName := strings.TrimSpace(out.Get("name").String())
								if outName != "" && outName == expectedName {
									used[outIdx] = true
									matchedForPending[pendingIdx] = outIdx
									break
								}
							}
						}
					}
				}

				// Pass 3: FIFO fallback for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx := range pendingCallIDs {
					pendingID := pendingCallIDs[pendingIdx]
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					for outIdx, out := range outputs {
						if !used[outIdx] && ExtractResponsesCallID(out) == "" {
							outName := strings.TrimSpace(out.Get("name").String())
							expectedName := pendingCallNames[pendingID]
							if outName == "" || expectedName == "" || outName == expectedName {
								used[outIdx] = true
								matchedForPending[pendingIdx] = outIdx
								break
							}
						}
					}
				}

				// Apply matched call_ids to outputs
				var remainingPending []string
				for pendingIdx, pendingID := range pendingCallIDs {
					outIdx := matchedForPending[pendingIdx]
					if outIdx < 0 {
						remainingPending = append(remainingPending, pendingID)
						continue
					}
					matchedOut := outputs[outIdx]
					if matchedOut.Get("call_id").String() != pendingID {
						raw := []byte(matchedOut.Raw)
						raw, _ = sjson.SetBytes(raw, "call_id", pendingID)
						normalized[start+outIdx] = gjson.ParseBytes(raw)
					}
				}
				pendingCallIDs = remainingPending
			}

		default:
			i++
		}
	}

	return normalized
}

// SanitizeOrphanResponsesToolOutputsDoc rewrites standalone function_call_output
// and custom_tool_call_output items that carry no call_id into user message
// items. Codex Desktop Companion Page context is injected as function_call_output
// items without call_id, which strict Responses upstreams reject with HTTP 400
// ("responses function_call_output call_id is required").
func SanitizeOrphanResponsesToolOutputsDoc(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	return sanitizeOrphanResponsesToolOutputs(body, input)
}

// SanitizeOrphanResponsesToolOutputsInput rewrites orphan tool outputs for a
// caller that already has the input array in hand. It returns the new body and
// whether anything changed.
func SanitizeOrphanResponsesToolOutputsInput(body []byte, input gjson.Result) ([]byte, bool) {
	if !input.IsArray() {
		return body, false
	}
	return sanitizeOrphanResponsesToolOutputs(body, input)
}

func sanitizeOrphanResponsesToolOutputs(body []byte, input gjson.Result) ([]byte, bool) {
	inputItems := input.Array()
	if len(inputItems) == 0 {
		return body, false
	}

	normalizedItems := NormalizeResponsesToolCallOutputs(inputItems)

	changed := false
	pendingMissingCallIDOutputType := ""
	for i := 0; i < len(normalizedItems); i++ {
		item := normalizedItems[i]
		itemType := strings.TrimSpace(item.Get("type").String())
		// A tool call without a call_id sets the expectation that the next output
		// of the matching type belongs to it and must not be rewritten here; the
		// provider-specific normalizer drops the pair.
		if itemType == "function_call" || itemType == "custom_tool_call" {
			if ExtractResponsesCallID(item) == "" {
				pendingMissingCallIDOutputType = itemType + "_output"
			} else {
				pendingMissingCallIDOutputType = ""
			}
			continue
		}
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			pendingMissingCallIDOutputType = ""
			continue
		}
		if isCodexDelegationToolOutput(item) {
			pendingMissingCallIDOutputType = ""
			continue
		}
		if resolvedCallID := responsesToolOutputExplicitCallID(item); resolvedCallID != "" {
			pendingMissingCallIDOutputType = ""
			origCallID := strings.TrimSpace(inputItems[i].Get("call_id").String())
			if origCallID != resolvedCallID {
				itemPath := "input." + strconv.Itoa(i) + ".call_id"
				if updated, errSet := sjson.SetBytes(body, itemPath, resolvedCallID); errSet == nil {
					body = updated
					changed = true
				}
			}
			continue
		}
		if pendingMissingCallIDOutputType == itemType &&
			strings.TrimSpace(item.Get("name").String()) == "" &&
			strings.TrimSpace(item.Get("id").String()) == "" {
			pendingMissingCallIDOutputType = ""
			continue
		}
		pendingMissingCallIDOutputType = ""

		if updated, ok := setOrphanResponsesToolOutputMessage(body, i, item); ok {
			body = updated
			changed = true
		}
	}
	return body, changed
}

func responsesToolOutputExplicitCallID(item gjson.Result) string {
	for _, key := range []string{"call_id", "tool_call_id", "callId"} {
		val := item.Get(key)
		if val.Type == gjson.String {
			if s := strings.TrimSpace(val.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

// isCodexDelegationToolOutput reports Codex Desktop delegation outputs, which the
// upstream consumes without a call_id and must keep their original shape.
func isCodexDelegationToolOutput(item gjson.Result) bool {
	if strings.TrimSpace(item.Get("type").String()) != "function_call_output" {
		return false
	}
	if strings.TrimSpace(item.Get("namespace").String()) != "codex_app" {
		return false
	}
	switch strings.TrimSpace(item.Get("name").String()) {
	case "create_thread", "send_message_to_thread":
		return true
	default:
		return false
	}
}

func setOrphanResponsesToolOutputMessage(body []byte, index int, item gjson.Result) ([]byte, bool) {
	itemPath := "input." + strconv.Itoa(index)
	fullText, extraParts := buildOrphanResponsesToolOutputContent(item)
	updated, errSet := sjson.SetRawBytes(body, itemPath, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`))
	if errSet != nil {
		return body, false
	}
	if updated, errSet = sjson.SetBytes(updated, itemPath+".content.0.text", fullText); errSet != nil {
		return body, false
	}
	for idx, partRaw := range extraParts {
		partPath := itemPath + ".content." + strconv.Itoa(idx+1)
		if updated, errSet = sjson.SetRawBytes(updated, partPath, partRaw); errSet != nil {
			return body, false
		}
	}
	return updated, true
}

func buildOrphanResponsesToolOutputContent(item gjson.Result) (string, [][]byte) {
	name := strings.TrimSpace(item.Get("name").String())
	if name == "" {
		name = "tool"
	}
	outputText, extraParts := extractOrphanResponsesToolOutputParts(item.Get("output"))
	if outputText == "" {
		outputText = "(empty)"
	}
	return "Tool output from " + name + ":\n" + outputText, extraParts
}

func extractOrphanResponsesToolOutputParts(output gjson.Result) (string, [][]byte) {
	if !output.Exists() || output.Type == gjson.Null {
		return "", nil
	}
	if output.Type == gjson.String {
		return strings.TrimSpace(output.String()), nil
	}
	if !output.IsArray() {
		return strings.TrimSpace(output.Raw), nil
	}
	var parts []string
	var extraParts [][]byte
	for _, part := range output.Array() {
		if part.Type == gjson.String {
			if s := strings.TrimSpace(part.String()); s != "" {
				parts = append(parts, s)
			}
			continue
		}
		if !part.IsObject() {
			continue
		}
		matchedText := false
		for _, key := range []string{"text", "input_text", "output_text"} {
			val := part.Get(key)
			if val.Type == gjson.String {
				matchedText = true
				if s := strings.TrimSpace(val.String()); s != "" {
					parts = append(parts, s)
				}
				break
			}
		}
		if matchedText {
			continue
		}
		// Keep non-text parts (images and other multimodal blocks) verbatim so no
		// content is lost when the item becomes a user message.
		if raw := strings.TrimSpace(part.Raw); raw != "" {
			extraParts = append(extraParts, []byte(raw))
		}
	}
	return strings.Join(parts, "\n"), extraParts
}
