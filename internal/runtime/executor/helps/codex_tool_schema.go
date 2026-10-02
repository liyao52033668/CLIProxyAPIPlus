package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	// codexComplexUnionBranchThreshold is the minimum number of union branches (oneOf / anyOf)
	// required before considering a pure constant union eligible for semantic enum normalization.
	codexComplexUnionBranchThreshold = 8
)

// IsCodexUserAgent reports whether headers contain a User-Agent indicating a Codex client.
func IsCodexUserAgent(headers http.Header) bool {
	if headers == nil {
		return false
	}
	ua := headerValueCaseInsensitive(headers, "User-Agent")
	if ua == "" {
		return false
	}
	return strings.Contains(strings.ToLower(ua), "codex")
}

// codexClientToolIntegerFields lists builtin Codex client tools whose numeric
// parameters must be declared as integers because the client deserializer
// rejects fractional JSON numbers for them.
var codexClientToolIntegerFields = map[string]map[string]struct{}{
	"exec_command": {
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
		"timeout_ms":        struct{}{},
	},
	"write_stdin": {
		"session_id":        struct{}{},
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
	},
	"sleep": {
		"duration_ms": struct{}{},
	},
	"wait_agent": {
		"timeout_ms": struct{}{},
	},
	"wait": {
		"yield_time_ms": struct{}{},
		"max_tokens":    struct{}{},
	},
	"tool_search": {
		"limit": struct{}{},
	},
	"test_sync_tool": {
		"sleep_before_ms": struct{}{},
		"sleep_after_ms":  struct{}{},
		"participants":    struct{}{},
		"timeout_ms":      struct{}{},
	},
}

func matchCodexTargetTool(toolName string) map[string]struct{} {
	baseName := strings.TrimSpace(toolName)
	if strings.HasPrefix(baseName, "functions__") {
		baseName = strings.TrimPrefix(baseName, "functions__")
	} else if strings.HasPrefix(baseName, "collab__") {
		baseName = strings.TrimPrefix(baseName, "collab__")
	}
	return codexClientToolIntegerFields[baseName]
}

func normalizeCodexToolFieldTypes(rawParams []byte, targetFields map[string]struct{}) ([]byte, bool) {
	if len(targetFields) == 0 || len(rawParams) == 0 {
		return rawParams, false
	}
	params := gjson.ParseBytes(rawParams)
	properties := params.Get("properties")
	if !properties.Exists() || !properties.IsObject() {
		return rawParams, false
	}
	changed := false
	for fieldName := range targetFields {
		prop := properties.Get(fieldName)
		if !prop.Exists() {
			continue
		}
		typeVal := prop.Get("type")
		if !typeVal.Exists() {
			continue
		}
		escapedKey := escapeCodexSjsonKey(fieldName)
		if typeVal.Type == gjson.String && typeVal.String() == "number" {
			if updated, errSet := sjson.SetBytes(rawParams, "properties."+escapedKey+".type", "integer"); errSet == nil {
				rawParams = updated
				changed = true
			}
		} else if typeVal.IsArray() {
			arrayItems := typeVal.Array()
			hasNumber := false
			seenTypes := make(map[string]struct{}, len(arrayItems))
			newTypes := make([]string, 0, len(arrayItems))
			for _, item := range arrayItems {
				itemStr := item.String()
				if itemStr == "number" {
					hasNumber = true
					itemStr = "integer"
				}
				if _, seen := seenTypes[itemStr]; !seen {
					seenTypes[itemStr] = struct{}{}
					newTypes = append(newTypes, itemStr)
				}
			}
			if hasNumber {
				if updated, errSet := sjson.SetBytes(rawParams, "properties."+escapedKey+".type", newTypes); errSet == nil {
					rawParams = updated
					changed = true
				}
			}
		}
	}
	return rawParams, changed
}

// NormalizeCodexToolIntegerTypes normalizes specified tool parameter declarations
// from number to integer for Codex clients across supported tool formats (OpenAI,
// Claude input_schema, Gemini function_declarations, and namespace tools).
// It must only run for non-Codex upstream targets; Codex upstreams strictly
// validate reserved tool schemas and reject rewritten types.
func NormalizeCodexToolIntegerTypes(body []byte, headers http.Header) []byte {
	if len(body) == 0 || !IsCodexUserAgent(headers) {
		return body
	}

	changed := false

	// 1. Process top-level tools
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.IsArray() {
		if updated, ok := normalizeToolIntegerTypesInArray(toolsResult); ok {
			if out, errSet := sjson.SetRawBytes(body, "tools", updated); errSet == nil {
				body = out
				changed = true
			}
		}
	}

	// 2. Process input[].additional_tools
	inputResult := gjson.GetBytes(body, "input")
	if inputResult.IsArray() {
		for idx, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				addTools := item.Get("tools")
				if addTools.IsArray() {
					if updated, ok := normalizeToolIntegerTypesInArray(addTools); ok {
						path := fmt.Sprintf("input.%d.tools", idx)
						if out, errSet := sjson.SetRawBytes(body, path, updated); errSet == nil {
							body = out
							changed = true
						}
					}
				}
			}
		}
	}

	if changed {
		log.Debugf("codex: normalized target tool number types to integer")
	}
	return body
}

func normalizeToolIntegerTypesInArray(tools gjson.Result) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	var out []byte
	offset := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		updated, changed := normalizeToolIntegerTypesInElement(tool)
		if !changed {
			return true
		}
		if out == nil {
			out = make([]byte, 0, len(tools.Raw))
		}
		start := tool.Index - tools.Index
		out = append(out, tools.Raw[offset:start]...)
		out = append(out, updated...)
		offset = start + len(tool.Raw)
		return true
	})
	if out == nil {
		return nil, false
	}
	return append(out, tools.Raw[offset:]...), true
}

func normalizeToolIntegerTypesInElement(tool gjson.Result) ([]byte, bool) {
	toolRaw := []byte(tool.Raw)
	changed := false

	// Handle namespace tools
	if tool.Get("type").String() == "namespace" {
		nested := tool.Get("tools")
		if nested.IsArray() {
			if updated, ok := normalizeToolIntegerTypesInArray(nested); ok {
				if out, errSet := sjson.SetRawBytes(toolRaw, "tools", updated); errSet == nil {
					toolRaw = out
					changed = true
				}
			}
		}
		return toolRaw, changed
	}

	// Handle Gemini function declarations
	for _, declKey := range []string{"function_declarations", "functionDeclarations"} {
		decls := tool.Get(declKey)
		if decls.IsArray() {
			if updated, ok := normalizeToolIntegerTypesInArray(decls); ok {
				if out, errSet := sjson.SetRawBytes(toolRaw, declKey, updated); errSet == nil {
					toolRaw = out
					changed = true
				}
			}
			return toolRaw, changed
		}
	}

	// Standard function/custom tool or Claude tool
	toolName := tool.Get("name").String()
	paramPath := "parameters"
	params := tool.Get("parameters")

	if !params.Exists() || !params.IsObject() {
		if fnParams := tool.Get("function.parameters"); fnParams.Exists() && fnParams.IsObject() {
			paramPath = "function.parameters"
			params = fnParams
			if toolName == "" {
				toolName = tool.Get("function.name").String()
			}
		} else if inputSchema := tool.Get("input_schema"); inputSchema.Exists() && inputSchema.IsObject() {
			paramPath = "input_schema"
			params = inputSchema
		} else if jsonSchema := tool.Get("parametersJsonSchema"); jsonSchema.Exists() && jsonSchema.IsObject() {
			paramPath = "parametersJsonSchema"
			params = jsonSchema
		} else {
			return nil, false
		}
	}

	targetFields := matchCodexTargetTool(toolName)
	if len(targetFields) == 0 {
		return nil, false
	}

	updatedParams, paramsChanged := normalizeCodexToolFieldTypes([]byte(params.Raw), targetFields)
	if !paramsChanged {
		return nil, false
	}

	updatedTool, errSet := sjson.SetRawBytes(toolRaw, paramPath, updatedParams)
	if errSet != nil {
		return nil, false
	}
	return updatedTool, true
}

// NormalizeCodexToolSchemas inspects function tools in a Codex request payload
// and simplifies pure constant union combinations (e.g. large oneOf branch sets
// representing enums with descriptions, as emitted by MCP servers) into semantically
// equivalent enum lists.
// Only unions mathematically proven to be semantically equivalent to enum definitions
// are modified; all other structures, property names, types, and constraints remain untouched.
// It deliberately does not normalize number types to integer: Codex upstreams
// strictly validate reserved tool schemas, so integer normalization is applied
// separately only for non-Codex targets (see NormalizeCodexToolIntegerTypes).
func NormalizeCodexToolSchemas(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return body
	}

	toolsArray := tools.Array()
	changed := false
	for i, tool := range toolsArray {
		updatedTool, toolChanged := normalizeCodexTool(tool)
		if toolChanged {
			var errSet error
			body, errSet = sjson.SetRawBytes(body, "tools."+strconv.Itoa(i), updatedTool)
			if errSet == nil {
				changed = true
			}
		}
	}
	if changed {
		log.Debugf("codex: normalized tool schemas to prevent upstream failure")
	}
	return body
}

func normalizeCodexTool(tool gjson.Result) ([]byte, bool) {
	toolType := tool.Get("type").String()
	// Handle namespace tools (e.g. multi-agent nested tools)
	if toolType == "namespace" {
		nestedTools := tool.Get("tools")
		if nestedTools.IsArray() && len(nestedTools.Array()) > 0 {
			changed := false
			raw := []byte(tool.Raw)
			for j, nestedTool := range nestedTools.Array() {
				updatedNested, nestedChanged := normalizeCodexTool(nestedTool)
				if nestedChanged {
					var errSet error
					raw, errSet = sjson.SetRawBytes(raw, "tools."+strconv.Itoa(j), updatedNested)
					if errSet == nil {
						changed = true
					}
				}
			}
			return raw, changed
		}
		return nil, false
	}

	if toolType != "function" && toolType != "custom" {
		return nil, false
	}

	params := tool.Get("parameters")
	if !params.Exists() || !params.IsObject() {
		return nil, false
	}

	rawTool := []byte(tool.Raw)
	updatedParams, paramsChanged := normalizeCodexParameters(params)
	if !paramsChanged {
		return nil, false
	}

	updatedTool, errSet := sjson.SetRawBytes(rawTool, "parameters", updatedParams)
	if errSet != nil {
		return nil, false
	}

	log.Debugf("codex: normalized schema for tool %s to avoid upstream abort", tool.Get("name").String())
	return updatedTool, true
}

func normalizeCodexParameters(params gjson.Result) ([]byte, bool) {
	rawParams := []byte(params.Raw)
	changed := false

	if sanitizedParams, patternChanged := stripIncompatiblePatternsFromJSON(rawParams); patternChanged {
		rawParams = sanitizedParams
		changed = true
		params = gjson.ParseBytes(rawParams)
	}

	properties := params.Get("properties")
	if properties.Exists() && properties.IsObject() {
		for propName, propVal := range properties.Map() {
			updatedProp, propChanged := normalizeCodexPropertySchema(propVal)
			if propChanged {
				escapedKey := escapeCodexSjsonKey(propName)
				var errSet error
				rawParams, errSet = sjson.SetRawBytes(rawParams, "properties."+escapedKey, updatedProp)
				if errSet == nil {
					changed = true
				}
			}
		}
	}

	return rawParams, changed
}

// stripIncompatiblePatternsFromJSON recursively removes pattern attributes that strict
// upstream validators reject: unsupported Unicode property escapes (\p{...} / \P{...})
// and the octal NUL escape (\0). See util.HasUnsupportedUnicodePropertyEscape for the
// predicate and the upstream errors behind each one.
// It is schema-aware: only subschemas under known JSON Schema keyword locations are visited,
// preventing accidental deletion of 'pattern' keys inside user data (e.g. description, default, enum).
func stripIncompatiblePatternsFromJSON(raw []byte) ([]byte, bool) {
	rawStr := string(raw)
	// The fast path avoids parsing when no candidate escape is present. Patterns
	// arrive JSON-escaped, so a literal backslash before '0' is `\\0` in the raw bytes.
	if !strings.Contains(rawStr, `\p{`) && !strings.Contains(rawStr, `\P{`) && !strings.Contains(rawStr, `\u`) &&
		!strings.Contains(rawStr, `\\0`) {
		return raw, false
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil || root == nil {
		return raw, false
	}
	// Verify no trailing garbage
	var dummy any
	if err := dec.Decode(&dummy); err != io.EOF {
		return raw, false
	}
	if !stripIncompatiblePatterns(root) {
		return raw, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return raw, false
	}
	return bytes.TrimSpace(buf.Bytes()), true
}

func stripIncompatiblePatterns(v any) bool {
	changed := false
	switch schema := v.(type) {
	case map[string]any:
		if patternVal, ok := schema["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(schema, "pattern")
			changed = true
		}

		// Inspect regex keys under patternProperties
		if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
					changed = true
				} else if stripIncompatiblePatterns(subSchema) {
					changed = true
				}
			}
		}

		for _, mapKey := range util.SchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := schema[mapKey].(map[string]any); ok {
				for _, subSchema := range subMap {
					if stripIncompatiblePatterns(subSchema) {
						changed = true
					}
				}
			}
		}

		for _, valKey := range util.SchemaValueKeywords {
			if val, exists := schema[valKey]; exists {
				switch sub := val.(type) {
				case map[string]any:
					if stripIncompatiblePatterns(sub) {
						changed = true
					}
				case []any:
					for _, item := range sub {
						if stripIncompatiblePatterns(item) {
							changed = true
						}
					}
				}
			}
		}
	case []any:
		for _, item := range schema {
			if stripIncompatiblePatterns(item) {
				changed = true
			}
		}
	}
	return changed
}

func normalizeCodexPropertySchema(prop gjson.Result) ([]byte, bool) {
	if !prop.IsObject() {
		return nil, false
	}

	hasOneOf := prop.Get("oneOf").Exists()
	hasAnyOf := prop.Get("anyOf").Exists()
	// If both oneOf and anyOf are present on the same property, leave untouched to preserve compound constraints
	if hasOneOf && hasAnyOf {
		return nil, false
	}

	unionName := ""
	if hasOneOf {
		unionName = "oneOf"
	} else if hasAnyOf {
		unionName = "anyOf"
	} else {
		return nil, false
	}

	union := prop.Get(unionName)
	if !union.IsArray() || len(union.Array()) < codexComplexUnionBranchThreshold {
		return nil, false
	}

	branches := union.Array()
	constRawValues := make([]string, 0, len(branches))
	constSemanticKeys := make([]string, 0, len(branches))
	seenSemanticKeys := make(map[string]struct{}, len(branches))
	pureConsts := true

	for _, branch := range branches {
		canonicalKey, rawJSON, ok := isPureConstBranch(branch)
		if !ok {
			pureConsts = false
			break
		}
		if _, seen := seenSemanticKeys[canonicalKey]; seen {
			// Duplicate semantic value in oneOf violates exclusivity; keep original schema
			pureConsts = false
			break
		}
		seenSemanticKeys[canonicalKey] = struct{}{}
		constSemanticKeys = append(constSemanticKeys, canonicalKey)
		constRawValues = append(constRawValues, rawJSON)
	}

	// Only transform if every branch is proven to be a pure, unique const definition
	if !pureConsts || len(constRawValues) == 0 {
		return nil, false
	}

	rawProp := []byte(prop.Raw)
	existingEnum := prop.Get("enum")
	if existingEnum.Exists() && existingEnum.IsArray() {
		existingEnumKeys := make([]string, 0, len(existingEnum.Array()))
		for _, v := range existingEnum.Array() {
			key, ok := canonicalJSONValueKey(v)
			if !ok {
				return nil, false
			}
			existingEnumKeys = append(existingEnumKeys, key)
		}
		// Only remove the redundant union if existing enum is proven semantically identical
		if equalCanonicalSets(existingEnumKeys, constSemanticKeys) {
			rawProp, _ = sjson.DeleteBytes(rawProp, unionName)
			return rawProp, true
		}
		return nil, false
	}

	// Migrate the pure const union to an enum using raw JSON tokens to avoid any numeric precision loss
	rawEnumJSON := []byte("[" + strings.Join(constRawValues, ",") + "]")
	rawProp, errEnum := sjson.SetRawBytes(rawProp, "enum", rawEnumJSON)
	if errEnum != nil {
		return nil, false
	}
	rawProp, _ = sjson.DeleteBytes(rawProp, unionName)
	return rawProp, true
}

func isPureConstBranch(branch gjson.Result) (canonicalKey string, rawJSON string, ok bool) {
	if !branch.IsObject() {
		return "", "", false
	}
	constVal := branch.Get("const")
	if !constVal.Exists() {
		return "", "", false
	}
	// Verify no other schema validation constraints exist in this branch
	for key := range branch.Map() {
		if key != "const" && key != "description" && key != "title" {
			return "", "", false
		}
	}
	key, ok := canonicalJSONValueKey(constVal)
	if !ok {
		return "", "", false
	}
	return key, constVal.Raw, true
}

func canonicalJSONValueKey(val gjson.Result) (string, bool) {
	switch val.Type {
	case gjson.String:
		return "s:" + val.String(), true
	case gjson.Number:
		raw := strings.TrimSpace(val.Raw)
		var r big.Rat
		if _, ok := r.SetString(raw); ok {
			return "n:" + r.RatString(), true
		}
		return "n:" + raw, true
	case gjson.True:
		return "b:true", true
	case gjson.False:
		return "b:false", true
	case gjson.Null:
		return "null", true
	default:
		return "", false
	}
}

func equalCanonicalSets(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	setA := make(map[string]struct{}, len(a))
	for _, v := range a {
		setA[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := setA[v]; !ok {
			return false
		}
	}
	return len(setA) == len(a)
}

// escapeCodexSjsonKey escapes dots, colons, and backslashes in property keys so that sjson treats
// keys containing dots (e.g. "my.field") or colons (e.g. ":action") as a single literal key rather
// than nested paths or control syntax.
func escapeCodexSjsonKey(key string) string {
	key = strings.ReplaceAll(key, `\`, `\\`)
	key = strings.ReplaceAll(key, `.`, `\.`)
	key = strings.ReplaceAll(key, `:`, `\:`)
	return key
}
