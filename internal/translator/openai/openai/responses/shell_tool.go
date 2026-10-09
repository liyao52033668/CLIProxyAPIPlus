package responses

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// shellToolSyntheticName is the synthetic Chat Completions function name given
// to a Responses local shell tool declaration.
const shellToolSyntheticName = "__cpa_local_shell"

// responsesToolIndex is scoped to one request. The local fork only tracks the
// synthetic local-shell mapping; the full declaration index lives upstream.
type responsesToolIndex struct {
	shellChatName string
}

func newResponsesToolIndex(root gjson.Result) *responsesToolIndex {
	return &responsesToolIndex{shellChatName: responsesShellToolName([]byte(root.Raw))}
}

func (idx *responsesToolIndex) isShell(name string) bool {
	return idx != nil && idx.shellChatName != "" && name == idx.shellChatName
}

var errInvalidShellAction = errors.New("invalid shell action: expected nonempty commands strings and optional positive integer limits")

// responsesShellToolName returns the synthetic Chat Completions function name
// assigned to the local shell tool declared by the request, or "" when the
// request declares none. When a shell tool is declared it is the synthetic
// name, disambiguated with a numeric suffix if a user tool already claims it.
func responsesShellToolName(requestRawJSON []byte) string {
	if !responsesRequestDeclaresLocalShellTool(requestRawJSON) {
		return ""
	}
	reserved := responsesReservedChatToolNames(requestRawJSON)
	name := shellToolSyntheticName
	for suffix := 1; reserved[name]; suffix++ {
		name = shellToolSyntheticName + "_" + strconv.Itoa(suffix)
	}
	return name
}

// responsesRequestDeclaresLocalShellTool reports whether the request declares
// a local shell tool (type "shell" with environment.type "local") either at
// the top level or inside additional_tools input items. Namespaced shells are
// not supported and do not count.
func responsesRequestDeclaresLocalShellTool(requestRawJSON []byte) bool {
	if len(requestRawJSON) == 0 {
		return false
	}
	root := gjson.ParseBytes(requestRawJSON)
	found := false
	var scan func(tools gjson.Result)
	scan = func(tools gjson.Result) {
		if found || !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
				scan(tool.Get("tools"))
				return true
			}
			if isLocalShellToolDeclaration(tool) {
				found = true
				return false
			}
			return true
		})
	}
	scan(root.Get("tools"))
	if found {
		return true
	}
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return !found
		})
	}
	return found
}

func isLocalShellToolDeclaration(tool gjson.Result) bool {
	if strings.TrimSpace(tool.Get("type").String()) != "shell" {
		return false
	}
	return tool.Get("environment.type").String() == "local"
}

// responsesReservedChatToolNames collects the Chat Completions function names
// the request's user declarations produce, so the synthetic shell name can
// avoid colliding with them.
func responsesReservedChatToolNames(requestRawJSON []byte) map[string]bool {
	reserved := make(map[string]bool)
	if len(requestRawJSON) == 0 {
		return reserved
	}
	root := gjson.ParseBytes(requestRawJSON)
	var scan func(tools gjson.Result)
	scan = func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			switch strings.TrimSpace(tool.Get("type").String()) {
			case "namespace":
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				if children := tool.Get("tools"); children.Exists() && children.IsArray() {
					children.ForEach(func(_, child gjson.Result) bool {
						reserved[qualifyResponsesNamespaceToolName(namespaceName, responsesToolName(child))] = true
						return true
					})
				}
			case "shell":
				if isLocalShellToolDeclaration(tool) {
					return true
				}
				reserved[responsesToolName(tool)] = true
			default:
				if name := responsesToolName(tool); name != "" {
					reserved[name] = true
				}
			}
			return true
		})
	}
	scan(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return true
		})
	}
	return reserved
}

// shellHistoryNameForRequest returns the Chat Completions function name that
// historical shell_call items are replayed under, even when the current
// request no longer declares a shell tool: a replay-only synthetic name is
// reserved without adding an available declaration.
func shellHistoryNameForRequest(requestRawJSON []byte) string {
	if name := responsesShellToolName(requestRawJSON); name != "" {
		return name
	}
	reserved := responsesReservedChatToolNames(requestRawJSON)
	root := gjson.ParseBytes(requestRawJSON)
	var scanItems func(input gjson.Result)
	scanItems = func(input gjson.Result) {
		if !input.Exists() || !input.IsArray() {
			return
		}
		input.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "function_call", "custom_tool_call":
				reserved[canonicalResponsesToolName(requestRawJSON, item.Get("name").String())] = true
			case "additional_tools":
				scanItems(item.Get("tools"))
			}
			return true
		})
	}
	scanItems(root.Get("input"))
	name := shellToolSyntheticName
	for suffix := 1; reserved[name]; suffix++ {
		name = shellToolSyntheticName + "_" + strconv.Itoa(suffix)
	}
	return name
}

func convertResponsesShellToolToOpenAIChat(_ gjson.Result, name string) ([]byte, bool) {
	tool := []byte(`{"type":"function","function":{"name":"","description":"Request commands to execute in the client-provided local shell environment. Each commands entry is a complete shell command, not an argv element.","parameters":{"type":"object","properties":{"commands":{"type":"array","items":{"type":"string"},"minItems":1},"timeout_ms":{"type":"integer","minimum":1},"max_output_length":{"type":"integer","minimum":1}},"required":["commands"],"additionalProperties":false}}}`)
	tool, _ = sjson.SetBytes(tool, "function.name", name)
	return tool, true
}

// Normalize shell history before the existing call grouping and output pairing.
// Keep the entire output envelope as JSON text, including truncation metadata.
func shellHistory(requestRawJSON []byte, items []gjson.Result) []gjson.Result {
	if !containsShellHistoryItems(items) {
		return items
	}
	name := shellHistoryNameForRequest(requestRawJSON)
	for i, item := range items {
		raw := []byte(item.Raw)
		switch item.Get("type").String() {
		case "shell_call":
			if environment := item.Get("environment.type"); environment.Exists() && environment.String() != "local" {
				continue
			}
			raw, _ = sjson.SetBytes(raw, "type", "function_call")
			raw, _ = sjson.SetBytes(raw, "name", name)
			raw, _ = sjson.SetBytes(raw, "arguments", item.Get("action").Raw)
		case "shell_call_output":
			raw, _ = sjson.SetBytes(raw, "type", "function_call_output")
			raw, _ = sjson.SetBytes(raw, "output", item.Raw)
		default:
			continue
		}
		items[i] = gjson.ParseBytes(raw)
	}
	return items
}

func containsShellHistoryItems(items []gjson.Result) bool {
	for _, item := range items {
		switch item.Get("type").String() {
		case "shell_call", "shell_call_output":
			return true
		}
	}
	return false
}

func shellCallItem(callID, arguments, status string) ([]byte, error) {
	action := gjson.Parse(arguments)
	if !gjson.Valid(arguments) || !action.IsObject() {
		return nil, errInvalidShellAction
	}
	commands := action.Get("commands")
	if !commands.IsArray() || len(commands.Array()) == 0 {
		return nil, errInvalidShellAction
	}
	for _, command := range commands.Array() {
		if command.Type != gjson.String || strings.TrimSpace(command.String()) == "" {
			return nil, errInvalidShellAction
		}
	}
	valid := true
	action.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "commands":
		case "timeout_ms", "max_output_length":
			if value.Type != gjson.Null && (value.Type != gjson.Number || value.Float() <= 0 || math.Trunc(value.Float()) != value.Float()) {
				valid = false
			}
		default:
			valid = false
		}
		return valid
	})
	if !valid {
		return nil, errInvalidShellAction
	}
	item := shellCallPlaceholder(callID)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetRawBytes(item, "action", []byte(arguments))
	return item, nil
}

func shellCallPlaceholder(callID string) []byte {
	item := []byte(`{"id":"","type":"shell_call","status":"in_progress","call_id":"","action":{"commands":[]}}`)
	item, _ = sjson.SetBytes(item, "id", "sh_"+callID)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	return item
}

// responsesToolInputFailure builds a terminal Responses failure payload for
// invalid upstream tool arguments, specializing the message for shell actions.
func responsesToolInputFailure(responseID string, sequence int, err error) []byte {
	failure := []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"","object":"response","status":"failed","error":{"type":"server_error","code":"invalid_tool_arguments","message":"Invalid apply_patch tool arguments received from upstream.","param":null}}}`)
	failure, _ = sjson.SetBytes(failure, "response.id", responseID)
	failure, _ = sjson.SetBytes(failure, "sequence_number", sequence)
	if errors.Is(err, errInvalidShellAction) {
		failure, _ = sjson.SetBytes(failure, "response.error.message", errInvalidShellAction.Error())
	}
	return failure
}
