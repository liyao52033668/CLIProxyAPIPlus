// Package gemini provides request translation functionality for Antigravity to Gemini API compatibility.
// It handles parsing and transforming Antigravity API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Antigravity API format and Gemini API's expected format.
package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToAntigravity parses and transforms a Antigravity API request into Gemini API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Gemini API format
// 3. Converts system instructions to the expected format
// 4. Fixes CLI tool response format and grouping
//
// Parameters:
//   - modelName: The name of the model to use for the request (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Antigravity API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertGeminiRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	functionNameMap := util.SanitizedFunctionNameMap(inputRawJSON)
	// Keep the envelope in []byte form. Round-tripping through string copies the
	// entire request, which dominates allocations for large inline data. Fill the
	// small envelope fields first so the payload is only spliced in once.
	envelope, _ := sjson.SetBytes([]byte(`{"project":"","request":{},"model":""}`), "model", modelName)
	rawJSON, _ = sjson.SetRawBytes(envelope, "request", rawJSON)
	if util.GetGJSONBytesNoCopy(rawJSON, "request.model").Exists() {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.model")
	}

	fixedJSON, errFixCLIToolResponse := fixCLIToolResponse(rawJSON)
	if errFixCLIToolResponse != nil {
		return []byte{}
	}
	rawJSON = fixedJSON

	if systemInstructionResult := util.GetGJSONBytesNoCopy(rawJSON, "request.system_instruction"); systemInstructionResult.Exists() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.systemInstruction", []byte(systemInstructionResult.Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.system_instruction")
	}

	// Normalize roles in request.contents: default to valid values if missing/invalid
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if contents.Exists() {
		prevRole := ""
		idx := 0
		contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
			role := value.Get("role").String()
			valid := role == "user" || role == "model"
			if role == "" || !valid {
				var newRole string
				if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
					// Tool results must always stay on a user turn so the upstream
					// API can pair them with the preceding model functionCall turn.
					newRole = "user"
				} else if prevRole == "" {
					newRole = "user"
				} else if prevRole == "user" {
					newRole = "model"
				} else {
					newRole = "user"
				}
				path := fmt.Sprintf("request.contents.%d.role", idx)
				rawJSON, _ = sjson.SetBytes(rawJSON, path, newRole)
				role = newRole
			}
			prevRole = role
			idx++
			return true
		})
	}

	toolsResult := util.GetGJSONBytesNoCopy(rawJSON, "request.tools")
	if toolsResult.IsArray() {
		seenFunctionNames := make(map[string]struct{})
		for toolIndex := range toolsResult.Array() {
			for _, key := range []string{"functionDeclarations", "function_declarations"} {
				path := fmt.Sprintf("request.tools.%d.%s", toolIndex, key)
				declarations := gjson.GetBytes(rawJSON, path)
				if !declarations.IsArray() {
					continue
				}

				parts := make([]string, 0, len(declarations.Array()))
				for _, declaration := range declarations.Array() {
					name := declaration.Get("name").String()
					mappedName := util.MapSanitizedFunctionName(functionNameMap, name)
					if mappedName != "" {
						if _, exists := seenFunctionNames[mappedName]; exists {
							continue
						}
						seenFunctionNames[mappedName] = struct{}{}
					}

					declarationJSON := []byte(declaration.Raw)
					declarationJSON, _ = sjson.SetBytes(declarationJSON, "name", mappedName)
					if parameters := declaration.Get("parameters"); parameters.Exists() {
						declarationJSON, _ = sjson.SetRawBytes(declarationJSON, "parametersJsonSchema", []byte(parameters.Raw))
						declarationJSON, _ = sjson.DeleteBytes(declarationJSON, "parameters")
					}
					parts = append(parts, string(declarationJSON))
				}
				deduplicated := []byte("[" + strings.Join(parts, ",") + "]")
				var errSet error
				rawJSON, errSet = sjson.SetRawBytes(rawJSON, path, deduplicated)
				if errSet != nil {
					log.Warnf("failed to normalize function declarations in tool %d: %v", toolIndex, errSet)
				}
			}
		}
		rawJSON = removeEmptyGeminiFunctionTools(rawJSON)
	}
	rawJSON = rewriteGeminiFunctionNames(rawJSON, functionNameMap)

	if strings.Contains(strings.ToLower(modelName), "claude") {
		rawJSON = SanitizeAntigravityClaudeGeminiRequestSignatures(modelName, rawJSON)
	} else {
		rawJSON = signature.SanitizeGeminiRequestThoughtSignatures(rawJSON, "request.contents")
	}

	return common.AttachDefaultSafetySettings(rawJSON, "request.safetySettings")
}

func removeEmptyGeminiFunctionTools(rawJSON []byte) []byte {
	tools := util.GetGJSONBytesNoCopy(rawJSON, "request.tools")
	if tools.IsArray() && len(tools.Array()) == 0 {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.tools")
		return rawJSON
	}
	changed := false
	var cleanedTools [][]byte
	for _, tool := range tools.Array() {
		toolJSON := []byte(tool.Raw)
		if tool.IsObject() {
			for _, key := range []string{"functionDeclarations", "function_declarations"} {
				if declarations := tool.Get(key); declarations.IsArray() && len(declarations.Array()) == 0 {
					toolJSON, _ = sjson.DeleteBytes(toolJSON, key)
					changed = true
				}
			}
			if len(util.ParseGJSONBytesNoCopy(toolJSON).Map()) == 0 {
				changed = true
				continue
			}
		}
		cleanedTools = append(cleanedTools, toolJSON)
	}
	if !changed {
		return rawJSON
	}
	if len(cleanedTools) == 0 {
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.tools")
		return rawJSON
	}
	rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.tools", util.JoinRawArrayBytes(cleanedTools))
	return rawJSON
}

func rewriteGeminiFunctionNames(rawJSON []byte, functionNameMap map[string]string) []byte {
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	// Rebuilding the contents array copies every content and part, so only pay for
	// it once a name actually needs rewriting.
	if geminiFunctionNamesNeedRewrite(contents, functionNameMap) {
		for contentIndex, content := range contents.Array() {
			for partIndex, part := range content.Get("parts").Array() {
				for _, field := range []string{"functionCall", "functionResponse", "function_call", "function_response"} {
					nameResult := part.Get(field + ".name")
					name := nameResult.String()
					if name == "" {
						continue
					}
					mappedName := util.MapSanitizedFunctionName(functionNameMap, name)
					if nameResult.Type == gjson.String && mappedName == name {
						continue
					}
					path := fmt.Sprintf("request.contents.%d.parts.%d.%s.name", contentIndex, partIndex, field)
					rawJSON, _ = sjson.SetBytes(rawJSON, path, mappedName)
				}
			}
		}
	}
	for _, allowedPath := range []string{
		"request.toolConfig.functionCallingConfig.allowedFunctionNames",
		"request.tool_config.function_calling_config.allowed_function_names",
	} {
		allowedNames := util.GetGJSONBytesNoCopy(rawJSON, allowedPath)
		for index, name := range allowedNames.Array() {
			mappedName := util.MapSanitizedFunctionName(functionNameMap, name.String())
			if name.Type == gjson.String && mappedName == name.String() {
				continue
			}
			path := fmt.Sprintf("%s.%d", allowedPath, index)
			rawJSON, _ = sjson.SetBytes(rawJSON, path, mappedName)
		}
	}
	return rawJSON
}

// geminiFunctionNameFields lists the part fields that can carry a function name.
var geminiFunctionNameFields = []string{"functionCall", "functionResponse", "function_call", "function_response"}

// geminiFunctionNamesNeedRewrite reports whether any part carries a function name
// that must be remapped or coerced to a string.
func geminiFunctionNamesNeedRewrite(contents gjson.Result, functionNameMap map[string]string) bool {
	needsRewrite := false
	contents.ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			for _, field := range geminiFunctionNameFields {
				nameResult := part.Get(field + ".name")
				name := nameResult.String()
				if name == "" {
					continue
				}
				if nameResult.Type == gjson.String && util.MapSanitizedFunctionName(functionNameMap, name) == name {
					continue
				}
				needsRewrite = true
				return false
			}
			return true
		})
		return !needsRewrite
	})
	return needsRewrite
}

func SanitizeAntigravityClaudeGeminiRequestSignatures(modelName string, rawJSON []byte) []byte {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(rawJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		log.WithError(err).Debug("antigravity gemini translator: failed to parse request for Claude signature sanitize")
		return rawJSON
	}

	request, ok := root["request"].(map[string]any)
	if !ok {
		return rawJSON
	}
	contents, ok := request["contents"].([]any)
	if !ok {
		return rawJSON
	}

	changed := false
	rewrittenContents := make([]any, 0, len(contents))
	for contentIndex, contentValue := range contents {
		content, ok := contentValue.(map[string]any)
		if !ok {
			rewrittenContents = append(rewrittenContents, contentValue)
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			rewrittenContents = append(rewrittenContents, content)
			continue
		}

		isModelTurn := content["role"] == "model"
		rewrittenParts := make([]any, 0, len(parts))
		for partIndex, partValue := range parts {
			part, ok := partValue.(map[string]any)
			if !ok {
				rewrittenParts = append(rewrittenParts, partValue)
				continue
			}

			rawSignature, hasSignature := antigravityClaudeGeminiPartThoughtSignature(part)
			if hasFunctionResponsePart(part) {
				if hasSignature {
					changed = true
					deleteAntigravityClaudeGeminiPartThoughtSignatureFields(part)
					logAntigravityClaudeGeminiSignatureSanitize(modelName, "drop_signature", "functionResponse parts cannot replay Claude thinking signatures", contentIndex, partIndex, rawSignature)
				}
				rewrittenParts = append(rewrittenParts, part)
				continue
			}
			if !isModelTurn {
				if hasSignature {
					changed = true
					deleteAntigravityClaudeGeminiPartThoughtSignatureFields(part)
					logAntigravityClaudeGeminiSignatureSanitize(modelName, "drop_signature", "non-model parts cannot replay Claude thinking signatures", contentIndex, partIndex, rawSignature)
				}
				rewrittenParts = append(rewrittenParts, part)
				continue
			}

			if part["thought"] == true {
				normalized, compatible := signature.CompatibleAntigravityClaudeThinkingSignature(rawSignature)
				if !compatible {
					changed = true
					logAntigravityClaudeGeminiSignatureSanitize(modelName, "drop_thinking_block", "missing_or_incompatible_signature", contentIndex, partIndex, rawSignature)
					continue
				}
				if text, _ := part["text"].(string); strings.TrimSpace(text) == "" {
					changed = true
					logAntigravityClaudeGeminiSignatureSanitize(modelName, "drop_thinking_block", "empty_thinking_text", contentIndex, partIndex, rawSignature)
					continue
				}
				if normalized != rawSignature {
					changed = true
					logAntigravityClaudeGeminiSignatureSanitize(modelName, "normalize_signature", "compatible_claude_signature", contentIndex, partIndex, rawSignature)
				}
				deleteAntigravityClaudeGeminiPartThoughtSignatureFields(part)
				part["thoughtSignature"] = normalized
				rewrittenParts = append(rewrittenParts, part)
				continue
			}

			if hasSignature {
				changed = true
				deleteAntigravityClaudeGeminiPartThoughtSignatureFields(part)
				logAntigravityClaudeGeminiSignatureSanitize(modelName, "drop_signature", "non-thinking parts should not carry Claude thinking signatures", contentIndex, partIndex, rawSignature)
			}
			rewrittenParts = append(rewrittenParts, part)
		}

		if len(rewrittenParts) == 0 {
			changed = true
			continue
		}
		content["parts"] = rewrittenParts
		rewrittenContents = append(rewrittenContents, content)
	}

	if !changed {
		return rawJSON
	}
	request["contents"] = rewrittenContents
	out, err := json.Marshal(root)
	if err != nil {
		log.WithError(err).Debug("antigravity gemini translator: failed to marshal Claude signature sanitize")
		return rawJSON
	}
	return out
}

func antigravityClaudeGeminiPartThoughtSignature(part map[string]any) (string, bool) {
	for _, path := range [][]string{
		{"thoughtSignature"},
		{"thought_signature"},
		{"functionCall", "thoughtSignature"},
		{"functionCall", "thought_signature"},
		{"functionResponse", "thoughtSignature"},
		{"functionResponse", "thought_signature"},
		{"extra_content", "google", "thought_signature"},
	} {
		if value, ok := stringAtPath(part, path...); ok {
			return value, true
		}
	}
	return "", false
}

func deleteAntigravityClaudeGeminiPartThoughtSignatureFields(part map[string]any) {
	for _, path := range [][]string{
		{"thoughtSignature"},
		{"thought_signature"},
		{"functionCall", "thoughtSignature"},
		{"functionCall", "thought_signature"},
		{"functionResponse", "thoughtSignature"},
		{"functionResponse", "thought_signature"},
		{"extra_content", "google", "thought_signature"},
	} {
		deleteAtPath(part, path...)
	}
}

func hasFunctionResponsePart(part map[string]any) bool {
	_, ok := part["functionResponse"]
	if ok {
		return true
	}
	_, ok = part["function_response"]
	return ok
}

func stringAtPath(value map[string]any, path ...string) (string, bool) {
	var current any = value
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = m[key]
		if !ok {
			return "", false
		}
	}
	s, ok := current.(string)
	return s, ok
}

func deleteAtPath(value map[string]any, path ...string) {
	if len(path) == 0 {
		return
	}
	current := value
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, path[len(path)-1])
}

func logAntigravityClaudeGeminiSignatureSanitize(modelName, action, reason string, contentIndex, partIndex int, rawSignature string) {
	fields := log.Fields{
		"component":         "signature_sanitizer",
		"translator":        "antigravity_gemini",
		"target_provider":   string(signature.SignatureProviderClaude),
		"action":            action,
		"reason":            reason,
		"model":             modelName,
		"content_index":     contentIndex,
		"part_index":        partIndex,
		"has_signature":     strings.TrimSpace(rawSignature) != "",
		"signature_length":  len(strings.TrimSpace(rawSignature)),
		"detected_provider": string(signature.DetectSignatureProviderForBlock(rawSignature, signature.SignatureBlockKindClaudeThinking)),
	}
	log.WithFields(fields).Debug("antigravity gemini translator: sanitized Claude target thoughtSignature before upstream")
}

// FunctionCallGroup represents a group of function calls and their responses
type FunctionCallGroup struct {
	ResponsesNeeded int
	CallNames       []string // ordered function call names for backfilling empty response names
}

func normalizeAntigravityInlineDataPart(part gjson.Result) ([]byte, bool) {
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if !inline.Exists() {
		return nil, false
	}
	data := inline.Get("data").String()
	if data == "" {
		return nil, false
	}
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	out := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	out, _ = sjson.SetBytes(out, "inlineData.mimeType", mimeType)
	out, _ = sjson.SetBytes(out, "inlineData.data", data)
	return out, true
}

func attachInlineDataToFunctionResponse(response gjson.Result, images [][]byte) gjson.Result {
	if len(images) == 0 {
		return response
	}
	target := []byte(response.Raw)
	for _, image := range images {
		target, _ = sjson.SetRawBytes(target, "functionResponse.parts.-1", image)
	}
	return gjson.ParseBytes(target)
}

func collectFunctionResponsesWithSiblingInlineData(parts gjson.Result) []gjson.Result {
	responses := make([]gjson.Result, 0)
	leadingImages := make([][]byte, 0)
	current := -1
	parts.ForEach(func(_, part gjson.Result) bool {
		if part.Get("functionResponse").Exists() {
			responses = append(responses, part)
			current = len(responses) - 1
			if len(leadingImages) > 0 {
				responses[current] = attachInlineDataToFunctionResponse(responses[current], leadingImages)
				leadingImages = nil
			}
			return true
		}
		imagePart, ok := normalizeAntigravityInlineDataPart(part)
		if !ok {
			return true
		}
		if current >= 0 {
			responses[current] = attachInlineDataToFunctionResponse(responses[current], [][]byte{imagePart})
		} else {
			leadingImages = append(leadingImages, imagePart)
		}
		return true
	})
	return responses
}

// parseFunctionResponseRaw attempts to normalize a function response part into a JSON object string.
// Falls back to a minimal "functionResponse" object when parsing fails.
// fallbackName is used when the response's own name is empty.
func parseFunctionResponseRaw(response gjson.Result, fallbackName string) string {
	if response.IsObject() && gjson.Valid(response.Raw) {
		raw := response.Raw
		name := response.Get("functionResponse.name").String()
		if strings.TrimSpace(name) == "" && fallbackName != "" {
			updated, _ := sjson.SetBytes([]byte(raw), "functionResponse.name", fallbackName)
			raw = string(updated)
		}
		return raw
	}

	log.Debugf("parse function response failed, using fallback")
	funcResp := response.Get("functionResponse")
	if funcResp.Exists() {
		fr := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
		name := funcResp.Get("name").String()
		if strings.TrimSpace(name) == "" {
			name = fallbackName
		}
		fr, _ = sjson.SetBytes(fr, "functionResponse.name", name)
		fr, _ = sjson.SetBytes(fr, "functionResponse.response.result", funcResp.Get("response").String())
		if id := funcResp.Get("id").String(); id != "" {
			fr, _ = sjson.SetBytes(fr, "functionResponse.id", id)
		}
		return string(fr)
	}

	useName := fallbackName
	if useName == "" {
		useName = "unknown"
	}
	fr := []byte(`{"functionResponse":{"name":"","response":{"result":""}}}`)
	fr, _ = sjson.SetBytes(fr, "functionResponse.name", useName)
	fr, _ = sjson.SetBytes(fr, "functionResponse.response.result", response.String())
	return string(fr)
}

// fixCLIToolResponse performs sophisticated tool response format conversion and grouping.
// This function transforms the CLI tool response format by intelligently grouping function calls
// with their corresponding responses, ensuring proper conversation flow and API compatibility.
// It converts from a linear format (1.json) to a grouped format (2.json) where function calls
// and their responses are properly associated and structured.
//
// Parameters:
//   - input: The input JSON string to be processed
//
// Returns:
//   - string: The processed JSON string with grouped function calls and responses
//   - error: An error if the processing fails
func fixCLIToolResponse(input []byte) ([]byte, error) {
	// Parse the input JSON to extract the conversation structure.
	// The parsed result references input directly; input must not be mutated
	// while the result and its raw slices are still in use.
	parsed := util.ParseGJSONBytesNoCopy(input)

	// Extract the contents array which contains the conversation messages
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		// log.Debugf(string(input))
		return input, fmt.Errorf("contents not found in input")
	}

	// Fast path: history without function responses does not need grouping, so
	// return the input unchanged to avoid rebuilding (and copying) the payload.
	needsGrouping := false
	allContentsAreObjects := true
	contents.ForEach(func(_, content gjson.Result) bool {
		if !content.IsObject() {
			allContentsAreObjects = false
			return true
		}
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				needsGrouping = true
				return false
			}
			return true
		})
		return !needsGrouping
	})
	if contents.IsArray() && allContentsAreObjects && !needsGrouping {
		return input, nil
	}

	// Initialize data structures for processing and grouping
	contentsWrapper := []byte(`{"contents":[]}`)
	var pendingGroups []*FunctionCallGroup // Groups awaiting completion with responses
	var collectedResponses []gjson.Result  // Standalone responses to be matched

	// Process each content object in the conversation
	// This iterates through messages and groups function calls with their responses
	contents.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		parts := value.Get("parts")

		// Check if this content has function responses
		responsePartsInThisContent := collectFunctionResponsesWithSiblingInlineData(parts)

		// If this content has function responses, collect them
		if len(responsePartsInThisContent) > 0 {
			collectedResponses = append(collectedResponses, responsePartsInThisContent...)

			// Check if pending groups can be satisfied (FIFO: oldest group first)
			for len(pendingGroups) > 0 && len(collectedResponses) >= pendingGroups[0].ResponsesNeeded {
				group := pendingGroups[0]
				pendingGroups = pendingGroups[1:]

				// Take the needed responses for this group
				groupResponses := collectedResponses[:group.ResponsesNeeded]
				collectedResponses = collectedResponses[group.ResponsesNeeded:]

				// Create merged function response content
				functionResponseContent := []byte(`{"parts":[],"role":"function"}`)
				for ri, response := range groupResponses {
					partRaw := parseFunctionResponseRaw(response, group.CallNames[ri])
					if partRaw != "" {
						functionResponseContent, _ = sjson.SetRawBytes(functionResponseContent, "parts.-1", []byte(partRaw))
					}
				}

				if gjson.GetBytes(functionResponseContent, "parts.#").Int() > 0 {
					contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", functionResponseContent)
				}
			}

			return true // Skip adding this content, responses are merged
		}

		// If this is a model with function calls, create a new group
		if role == "model" {
			var callNames []string
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					callNames = append(callNames, part.Get("functionCall.name").String())
				}
				return true
			})

			if len(callNames) > 0 {
				// Add the model content
				if !value.IsObject() {
					log.Warnf("failed to parse model content")
					return true
				}
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))

				// Create a new group for tracking responses
				group := &FunctionCallGroup{
					ResponsesNeeded: len(callNames),
					CallNames:       callNames,
				}
				pendingGroups = append(pendingGroups, group)
			} else {
				// Regular model content without function calls
				if !value.IsObject() {
					log.Warnf("failed to parse content")
					return true
				}
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))
			}
		} else {
			// Non-model content (user, etc.)
			if !value.IsObject() {
				log.Warnf("failed to parse content")
				return true
			}
			contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))
		}

		return true
	})

	// Handle any remaining pending groups with remaining responses
	for _, group := range pendingGroups {
		if len(collectedResponses) >= group.ResponsesNeeded {
			groupResponses := collectedResponses[:group.ResponsesNeeded]
			collectedResponses = collectedResponses[group.ResponsesNeeded:]

			functionResponseContent := []byte(`{"parts":[],"role":"function"}`)
			for ri, response := range groupResponses {
				partRaw := parseFunctionResponseRaw(response, group.CallNames[ri])
				if partRaw != "" {
					functionResponseContent, _ = sjson.SetRawBytes(functionResponseContent, "parts.-1", []byte(partRaw))
				}
			}

			if gjson.GetBytes(functionResponseContent, "parts.#").Int() > 0 {
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", functionResponseContent)
			}
		}
	}

	// Update the original JSON with the new contents
	result, _ := sjson.SetRawBytes(input, "request.contents", []byte(gjson.GetBytes(contentsWrapper, "contents").Raw))

	return result, nil
}
