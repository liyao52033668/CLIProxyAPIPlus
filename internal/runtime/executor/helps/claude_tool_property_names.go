package helps

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Claude rejects a request whose tools declare an input_schema property key
// outside ^[a-zA-Z0-9_.-]{1,64}$, while OpenAI-compatible clients and MCP
// servers commonly use parameter names such as "not[labels]". Because clients
// resend their tools on every turn, one such tool fails the whole conversation.
//
// Claude presents tool schemas to the model as part of its prompt, so such a
// tool is sent with an open object input_schema and its original schema
// embedded verbatim in the description. No parameter is renamed or dropped: the
// model sees every original name and constraint and calls the tool with the
// original names, so neither historical tool_use inputs nor tool calls in
// responses need any rewriting. Claude no longer enforces the embedded schema
// structurally, so strict is removed as structured outputs cannot apply it.
const (
	claudePropertyKeyMaxLength      = 64
	claudeToolSchemaMaxInspectDepth = 256
	claudeToolSchemaFallbackPrefix  = "The input object must conform to the following JSON Schema. Use its property names exactly as written, including any characters such as brackets:\n"
	claudeToolSchemaFallbackSchema  = `{"type":"object"}`
)

// claudePropertyKeyAccepted reports whether Claude accepts name as an
// input_schema property key.
func claudePropertyKeyAccepted(name string) bool {
	if len(name) == 0 || len(name) > claudePropertyKeyMaxLength {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// claudeSchemaHasRejectedPropertyKey reports whether schema, or any subschema
// reachable through JSON Schema applicator and definition keywords, declares a
// properties key Claude rejects. Keyword names are only interpreted at schema
// positions, so a definition or property named like a keyword is handled by its
// role. Values of other keywords, such as const, default, enum, examples and
// unknown annotations, are data and are not inspected. Schemas nested beyond
// the inspection depth are reported as rejected so they take the fallback,
// which still carries the full schema text.
func claudeSchemaHasRejectedPropertyKey(schema gjson.Result, depth int) bool {
	if !schema.IsObject() {
		return false
	}
	if depth > claudeToolSchemaMaxInspectDepth {
		return true
	}
	found := false
	subschema := func(value gjson.Result) bool {
		found = claudeSchemaHasRejectedPropertyKey(value, depth+1)
		return !found
	}
	subschemas := func(value gjson.Result) {
		value.ForEach(func(_, item gjson.Result) bool { return subschema(item) })
	}
	schema.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "properties":
			if !value.IsObject() {
				return true
			}
			value.ForEach(func(name, item gjson.Result) bool {
				if !claudePropertyKeyAccepted(name.String()) {
					found = true
					return false
				}
				return subschema(item)
			})
		case "$defs", "definitions", "patternProperties", "dependentSchemas", "dependencies":
			// Maps of named subschemas; dependencies may also hold name arrays,
			// which are not objects and are skipped by subschema.
			if value.IsObject() {
				subschemas(value)
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			if value.IsArray() {
				subschemas(value)
			}
		case "items":
			if value.IsArray() {
				subschemas(value)
			} else {
				subschema(value)
			}
		case "additionalProperties", "unevaluatedProperties", "additionalItems", "unevaluatedItems",
			"contains", "propertyNames", "not", "if", "then", "else", "contentSchema":
			subschema(value)
		}
		return !found
	})
	return found
}

// isClaudeServerToolType reports whether a tool declaration is an Anthropic
// server-side tool (which carries no input_schema and needs no fallback).
func isClaudeServerToolType(toolType string) bool {
	switch strings.ToLower(strings.TrimSpace(toolType)) {
	case "web_search_20250305", "web_search", "web_fetch_20250910", "web_fetch",
		"computer_20250124", "computer_use_preview", "bash_20250124", "bash",
		"text_editor_20250124", "text_editor_20250429", "text_editor",
		"code_execution_20250522", "code_execution", "memory_20250818",
		"tool_search_tool_regex_20251119", "tool_search_tool_bm25_20251119",
		"advisor", "server_tool_use", "mcp_toolset":
		return true
	}
	return false
}

// claudeToolNeedsSchemaFallback reports whether a Claude client tool
// declaration has an input_schema that Claude would reject.
func claudeToolNeedsSchemaFallback(tool gjson.Result) bool {
	if isClaudeServerToolType(tool.Get("type").String()) {
		return false
	}
	schema := tool.Get("input_schema")
	return schema.IsObject() && claudeSchemaHasRejectedPropertyKey(schema, 0)
}

// NormalizeClaudeToolPropertyNames applies the schema fallback to every client
// tool declared in tools[] or in an inline tool_addition definition whose
// input_schema has a property key Claude rejects. Tools Claude accepts are left
// unchanged. It must run before user payload rules.
func NormalizeClaudeToolPropertyNames(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	root := gjson.ParseBytes(body)
	var paths []string
	root.Get("tools").ForEach(func(index, tool gjson.Result) bool {
		if claudeToolNeedsSchemaFallback(tool) {
			paths = append(paths, fmt.Sprintf("tools.%d", index.Int()))
		}
		return true
	})
	root.Get("messages").ForEach(func(messageIndex, message gjson.Result) bool {
		message.Get("content").ForEach(func(partIndex, part gjson.Result) bool {
			if part.Get("type").String() != "tool_addition" || part.Get("tool.type").String() != "tool_definition" {
				return true
			}
			if claudeToolNeedsSchemaFallback(part.Get("tool.definition")) {
				paths = append(paths, fmt.Sprintf("messages.%d.content.%d.tool.definition", messageIndex.Int(), partIndex.Int()))
			}
			return true
		})
		return true
	})
	for _, path := range paths {
		body = applyClaudeToolSchemaFallback(body, path)
	}
	return body
}

func applyClaudeToolSchemaFallback(body []byte, path string) []byte {
	tool := gjson.GetBytes(body, path)
	name := tool.Get("name").String()
	var description strings.Builder
	if existing := tool.Get("description").String(); strings.TrimSpace(existing) != "" {
		description.WriteString(existing)
		description.WriteString("\n\n")
	}
	description.WriteString(claudeToolSchemaFallbackPrefix)
	description.WriteString(tool.Get("input_schema").Raw)
	hasStrict := tool.Get("strict").Exists()

	out, errSet := sjson.SetBytes(body, path+".description", description.String())
	if errSet != nil {
		log.Warnf("claude tool %q: failed to embed input_schema in description: %v", name, errSet)
		return body
	}
	body = out
	out, errSet = sjson.SetRawBytes(body, path+".input_schema", []byte(claudeToolSchemaFallbackSchema))
	if errSet != nil {
		log.Warnf("claude tool %q: failed to replace input_schema: %v", name, errSet)
		return body
	}
	body = out
	if hasStrict {
		out, errDel := sjson.DeleteBytes(body, path+".strict")
		if errDel != nil {
			log.Warnf("claude tool %q: failed to remove strict: %v", name, errDel)
		} else {
			body = out
		}
	}
	log.Debugf("claude tool %q: input_schema has property names Claude rejects; embedded the schema in the description", name)
	return body
}
