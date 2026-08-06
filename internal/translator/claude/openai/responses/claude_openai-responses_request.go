package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	user    = ""
	account = ""
	session = ""
)

// ConvertOpenAIResponsesRequestToClaude transforms an OpenAI Responses API request
// into a Claude Messages API request using only gjson/sjson for JSON handling.
// It supports:
// - instructions -> system message
// - input[].type==message with input_text/output_text -> user/assistant messages
// - function_call/custom_tool_call -> assistant tool_use
// - function_call_output/custom_tool_call_output -> user tool_result
// - top-level tools and input[].additional_tools -> Claude tools[].input_schema
// - max_output_tokens -> max_tokens
// - stream passthrough via parameter
func ConvertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	if account == "" {
		u, _ := uuid.NewRandom()
		account = u.String()
	}
	if session == "" {
		u, _ := uuid.NewRandom()
		session = u.String()
	}
	if user == "" {
		sum := sha256.Sum256([]byte(account + session))
		user = hex.EncodeToString(sum[:])
	}
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", user, account, session)

	// Base Claude message payload
	out := fmt.Appendf(nil, `{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID)

	root := gjson.ParseBytes(rawJSON)

	appendAssistantPart := func(partJSON []byte) {
		if len(partJSON) == 0 {
			return
		}
		messageCount := int(gjson.GetBytes(out, "messages.#").Int())
		if messageCount > 0 {
			lastPath := fmt.Sprintf("messages.%d", messageCount-1)
			if gjson.GetBytes(out, lastPath+".role").String() == "assistant" {
				lastContent := gjson.GetBytes(out, lastPath+".content")
				if lastContent.IsArray() {
					out, _ = sjson.SetRawBytes(out, lastPath+".content.-1", partJSON)
					return
				}
				if lastContent.Type == gjson.String {
					content := []byte(`[]`)
					if text := lastContent.String(); text != "" {
						textPart := []byte(`{"type":"text","text":""}`)
						textPart, _ = sjson.SetBytes(textPart, "text", text)
						content, _ = sjson.SetRawBytes(content, "-1", textPart)
					}
					content, _ = sjson.SetRawBytes(content, "-1", partJSON)
					out, _ = sjson.SetRawBytes(out, lastPath+".content", content)
					return
				}
			}
		}
		msg := []byte(`{"role":"assistant","content":[]}`)
		msg, _ = sjson.SetRawBytes(msg, "content.-1", partJSON)
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	// Convert OpenAI Responses reasoning.effort to Claude thinking config.
	if v := root.Get("reasoning.effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 supports adaptive thinking with output_config.effort.
			// MapToClaudeEffort normalizes levels (e.g. minimal→low, xhigh→high) to avoid
			// validation errors since validate treats same-provider unsupported levels as errors.
			if supportsAdaptive {
				switch effort {
				case "none":
					out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				case "auto":
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				default:
					if mapped, ok := thinking.MapToClaudeEffort(effort, supportsMax); ok {
						effort = mapped
					}
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.SetBytes(out, "output_config.effort", effort)
				}
			} else {
				// Legacy/manual thinking (budget_tokens).
				budget, ok := thinking.ConvertLevelToBudget(effort)
				if ok {
					switch budget {
					case 0:
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					case -1:
						out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
					default:
						if budget > 0 {
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				}
			}
		}
	}

	// Helper for generating tool call IDs when missing
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		for range 24 {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens
	if mot := root.Get("max_output_tokens"); mot.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", mot.Int())
	}

	// Stream
	out, _ = sjson.SetBytes(out, "stream", stream)

	// System-level inputs become canonical top-level Claude system blocks in
	// source order: instructions first, then every input item whose role is
	// system or developer. Each source block stays a separate Claude block and
	// keeps operator authority; the Claude executor decides the final placement
	// (mid-conversation role=system messages, or system reminders on legacy
	// models), so this layer must not merge, trim or downgrade them to user text.
	systemBlocks := make([]string, 0, 4)
	appendSystemText := func(text string, cacheSource gjson.Result) {
		if text == "" {
			return
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", text)
		if cacheSource.Exists() {
			block = common.AttachCacheControl(block, cacheSource)
		}
		systemBlocks = append(systemBlocks, string(block))
	}
	if instr := root.Get("instructions"); instr.Type == gjson.String {
		appendSystemText(instr.String(), gjson.Result{})
	}
	if input := root.Get("input"); input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if !isResponsesSystemLevelRole(item.Get("role").String()) {
				return true
			}
			startIdx := len(systemBlocks)
			content := item.Get("content")
			if content.Type == gjson.String {
				appendSystemText(content.String(), gjson.Result{})
			} else if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "input_text", "output_text", "text":
						appendSystemText(part.Get("text").String(), part)
					default:
						if block := responsesSystemUnsupportedBlock(part); len(block) > 0 {
							systemBlocks = append(systemBlocks, string(block))
						}
					}
					return true
				})
			}
			// Item-level cache_control applies to the last block this item produced.
			if item.Get("cache_control").Exists() && len(systemBlocks) > startIdx {
				lastIdx := len(systemBlocks) - 1
				if !gjson.GetBytes([]byte(systemBlocks[lastIdx]), "cache_control").Exists() {
					systemBlocks[lastIdx] = string(common.AttachCacheControl([]byte(systemBlocks[lastIdx]), item))
				}
			}
			return true
		})
	}

	// input array processing
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			// System-level items already became top-level system blocks.
			if isResponsesSystemLevelRole(item.Get("role").String()) {
				return true
			}
			typ := item.Get("type").String()
			if typ == "" && item.Get("role").String() != "" {
				typ = "message"
			}
			switch typ {
			case "message":
				// Determine role and construct Claude-compatible content parts.
				var role string
				var textAggregate strings.Builder
				var partsJSON []string
				hasImage := false
				hasFile := false
				if parts := item.Get("content"); parts.Exists() && parts.IsArray() {
					parts.ForEach(func(_, part gjson.Result) bool {
						ptype := part.Get("type").String()
						switch ptype {
						case "input_text", "output_text":
							if t := part.Get("text"); t.Exists() {
								txt := t.String()
								textAggregate.WriteString(txt)
								contentPart := []byte(`{"type":"text","text":""}`)
								contentPart, _ = sjson.SetBytes(contentPart, "text", txt)
								contentPart = common.AttachCacheControl(contentPart, part)
								partsJSON = append(partsJSON, string(contentPart))
							}
							if ptype == "input_text" {
								role = "user"
							} else {
								role = "assistant"
							}
						case "input_image":
							url := part.Get("image_url").String()
							if url == "" {
								url = part.Get("url").String()
							}
							if url != "" {
								var contentPart []byte
								if after, ok := strings.CutPrefix(url, "data:"); ok {
									trimmed := after
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									mediaType := "application/octet-stream"
									data := ""
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mediaType = mediaAndData[0]
										}
										data = mediaAndData[1]
									}
									if data != "" {
										contentPart = []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
										contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
										contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
										contentPart = common.AttachCacheControl(contentPart, part)
									}
								} else {
									contentPart = []byte(`{"type":"image","source":{"type":"url","url":""}}`)
									contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
								}
								if len(contentPart) > 0 {
									contentPart = common.AttachCacheControl(contentPart, part)
									partsJSON = append(partsJSON, string(contentPart))
									if role == "" {
										role = "user"
									}
									hasImage = true
								}
							}
						case "input_file":
							fileData := part.Get("file_data").String()
							if fileData != "" {
								mediaType := "application/octet-stream"
								data := fileData
								if after, ok := strings.CutPrefix(fileData, "data:"); ok {
									trimmed := after
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mediaType = mediaAndData[0]
										}
										data = mediaAndData[1]
									}
								}
								contentPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
								contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
								contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
								partsJSON = append(partsJSON, string(contentPart))
								if role == "" {
									role = "user"
								}
								hasFile = true
							}
						}
						return true
					})
				} else if parts.Type == gjson.String {
					textAggregate.WriteString(parts.String())
				}

				// Fallback to given role if content types not decisive
				if role == "" {
					r := item.Get("role").String()
					switch r {
					case "user", "assistant":
						role = r
					default:
						role = "user"
					}
				}

				if len(partsJSON) > 0 {
					if role == "assistant" {
						for _, partJSON := range partsJSON {
							appendAssistantPart([]byte(partJSON))
						}
					} else {
						msg := []byte(`{"role":"","content":[]}`)
						msg, _ = sjson.SetBytes(msg, "role", role)
						textPart := gjson.Parse(partsJSON[0])
						hasPartCacheControl := textPart.Get("cache_control").Exists()
						if len(partsJSON) == 1 && !hasImage && !hasFile && !hasPartCacheControl && !item.Get("cache_control").Exists() {
							msg, _ = sjson.DeleteBytes(msg, "content")
							textPart := gjson.Parse(partsJSON[0])
							msg, _ = sjson.SetBytes(msg, "content", textPart.Get("text").String())
						} else {
							for _, partJSON := range partsJSON {
								msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(partJSON))
							}
						}
						msg = common.AttachMessageCacheControl(msg, item)
						out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
					}
				} else if textAggregate.Len() > 0 {
					if role == "assistant" {
						textPart := []byte(`{"type":"text","text":""}`)
						textPart, _ = sjson.SetBytes(textPart, "text", textAggregate.String())
						appendAssistantPart(textPart)
					} else {
						msg := []byte(`{"role":"","content":""}`)
						msg, _ = sjson.SetBytes(msg, "role", role)
						msg, _ = sjson.SetBytes(msg, "content", textAggregate.String())
						out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
					}
				}

			case "function_call", "custom_tool_call":
				// Map to assistant tool_use. Freeform custom input is wrapped in an
				// object because Claude tool_use input must be a JSON object.
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = genToolCallID()
				}
				callID = util.SanitizeClaudeToolID(callID)
				name := item.Get("name").String()
				if namespaceName := strings.TrimSpace(item.Get("namespace").String()); namespaceName != "" {
					// Rebuild the qualified name emitted by the previous Responses turn.
					name = qualifyResponsesNamespaceToolName(namespaceName, name)
				}
				isCustomToolCall := typ == "custom_tool_call"

				toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolUse, _ = sjson.SetBytes(toolUse, "id", callID)
				toolUse, _ = sjson.SetBytes(toolUse, "name", name)
				if isCustomToolCall {
					toolUse, _ = sjson.SetBytes(toolUse, "input.input", item.Get("input").String())
				} else {
					argsStr := item.Get("arguments").String()
					if argsStr != "" && gjson.Valid(argsStr) {
						argsJSON := gjson.Parse(argsStr)
						if argsJSON.IsObject() {
							inputRaw := sanitizeResponsesToolUseInput(name, []byte(argsJSON.Raw))
							toolUse, _ = sjson.SetRawBytes(toolUse, "input", inputRaw)
						}
					}
				}

				// Append into the active assistant message so consecutive tool
				// uses share one turn (Claude rejects non-consecutive tool_use).
				appendAssistantPart(toolUse)

			case "function_call_output", "custom_tool_call_output":
				// Map to user tool_result. An empty call_id cannot match any
				// tool_use, so keep it empty instead of minting a random id.
				callID := item.Get("call_id").String()
				if callID != "" {
					callID = util.SanitizeClaudeToolID(callID)
				}
				outputStr := item.Get("output").String()
				toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
				toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", callID)
				toolResult, _ = sjson.SetBytes(toolResult, "content", outputStr)

				usr := []byte(`{"role":"user","content":[]}`)
				usr, _ = sjson.SetRawBytes(usr, "content.-1", toolResult)
				out, _ = sjson.SetRawBytes(out, "messages.-1", usr)

			case "reasoning":
				if partJSON := convertResponsesReasoningToClaudeThinking(item); partJSON != nil {
					appendAssistantPart(partJSON)
				}
			}
			return true
		})
	}

	// Preserve a minimal conversational turn for system-only inputs so downstream
	// validation still sees a Claude-shaped request.
	if gjson.GetBytes(out, "messages.#").Int() == 0 && len(systemBlocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "messages.-1", []byte(`{"role":"user","content":[{"type":"text","text":""}]}`))
	}
	if len(systemBlocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "system", []byte("["+strings.Join(systemBlocks, ",")+"]"))
	}

	includedToolNames := map[string]struct{}{}
	toolNameMap := map[string]string{}

	// Responses Lite puts tool definitions in input[].additional_tools. Select
	// one winner for each final name, while keeping the original order for the
	// tools that survive conversion. Compute descriptors/winners once and reuse,
	// since each call rescans the whole tool graph (top-level tools plus every
	// additional_tools source).
	descriptors := responsesToolDescriptors(root)
	winners := responsesToolWinnersFromDescriptors(descriptors)
	var toolItems [][]byte
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order {
			continue
		}
		tJSON, ok := convertResponsesToolDescriptorToClaude(descriptor)
		if !ok {
			continue
		}
		toolName := gjson.GetBytes(tJSON, "name").String()
		if toolName != "" {
			includedToolNames[toolName] = struct{}{}
		}
		toolItems = append(toolItems, tJSON)
	}
	toolNameMap = responsesToolNameMapFromDescriptors(descriptors, winners, includedToolNames)
	if len(toolItems) > 0 {
		toolsJSON := []byte("[]")
		for _, tJSON := range toolItems {
			toolsJSON, _ = sjson.SetRawBytes(toolsJSON, "-1", tJSON)
		}
		out, _ = sjson.SetRawBytes(out, "tools", toolsJSON)
	}

	// Map tool_choice similar to Chat Completions translator (optional in docs, safe to handle)
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch toolChoice.String() {
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "none":
				// Leave unset; implies no tools
			case "required":
				if len(includedToolNames) > 0 {
					out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
				}
			}
		case gjson.JSON:
			choiceType := toolChoice.Get("type").String()
			if choiceType == "function" || choiceType == "custom" {
				fn := toolChoice.Get("function.name").String()
				if fn == "" {
					fn = toolChoice.Get("custom.name").String()
				}
				if fn == "" {
					fn = toolChoice.Get("name").String()
				}
				namespaceName := toolChoice.Get("namespace").String()
				if namespaceName == "" {
					namespaceName = toolChoice.Get("function.namespace").String()
				}
				if namespaceName == "" {
					namespaceName = toolChoice.Get("custom.namespace").String()
				}
				if namespaceName != "" {
					fn = qualifyResponsesNamespaceToolName(namespaceName, fn)
				}
				if mappedName := toolNameMap[fn]; mappedName != "" {
					fn = mappedName
				}
				if _, ok := includedToolNames[fn]; ok {
					toolChoiceJSON := []byte(`{"name":"","type":"tool"}`)
					toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", fn)
					out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
				}
			}
		default:

		}
	}

	return out
}

func isOpenAIResponsesApplyPatchCustomTool(toolType string, tool gjson.Result) bool {
	return toolType == "custom" && strings.TrimSpace(tool.Get("name").String()) == "apply_patch"
}

func convertResponsesToolDescriptorToClaude(descriptor responsesToolDescriptor) ([]byte, bool) {
	overrideName := ""
	if !descriptor.direct {
		overrideName = descriptor.name
	}
	switch descriptor.toolType {
	case "function":
		return convertResponsesFunctionToolToClaude(descriptor.tool, overrideName)
	case "custom":
		return convertResponsesCustomToolToClaude(descriptor.tool, overrideName)
	case "web_search":
		return convertResponsesWebSearchToolToClaude(descriptor.tool)
	default:
		if isUnsupportedOpenAIBuiltinToolType(descriptor.toolType) {
			return nil, false
		}
		if descriptor.tool.Get("name").String() == "" {
			return nil, false
		}
		return []byte(descriptor.tool.Raw), true
	}
}

type responsesToolSource struct {
	tools    gjson.Result
	priority int // Top-level tools use 0; all additional_tools sources use 1.
}

func responsesToolSources(root gjson.Result) []responsesToolSource {
	var sources []responsesToolSource
	appendSource := func(tools gjson.Result, priority int) {
		if tools.Exists() && tools.IsArray() {
			sources = append(sources, responsesToolSource{tools: tools, priority: priority})
		}
	}

	appendSource(root.Get("tools"), 0)
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendSource(item.Get("tools"), 1)
			}
			return true
		})
	}
	return sources
}

type responsesToolDescriptor struct {
	name           string
	childName      string
	namespace      string
	toolType       string
	tool           gjson.Result
	sourcePriority int
	direct         bool
	order          int
}

func responsesToolDescriptors(root gjson.Result) []responsesToolDescriptor {
	var descriptors []responsesToolDescriptor
	appendDescriptor := func(tool gjson.Result, name, childName, namespaceName, toolType string, sourcePriority int, direct bool) {
		if name == "" {
			return
		}
		descriptors = append(descriptors, responsesToolDescriptor{
			name:           name,
			childName:      childName,
			namespace:      namespaceName,
			toolType:       toolType,
			tool:           tool,
			sourcePriority: sourcePriority,
			direct:         direct,
			order:          len(descriptors),
		})
	}
	appendNamespaceChildren := func(namespaceTool gjson.Result, sourcePriority int) {
		namespaceName := strings.TrimSpace(namespaceTool.Get("name").String())
		children := namespaceTool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			return
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
			switch strings.TrimSpace(child.Get("type").String()) {
			case "", "function":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "function", sourcePriority, false)
			case "custom":
				if !isOpenAIResponsesApplyPatchCustomTool("custom", child) {
					appendDescriptor(child, qualifiedName, childName, namespaceName, "custom", sourcePriority, false)
				}
			}
			return true
		})
	}
	for _, source := range responsesToolSources(root) {
		source.tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := strings.TrimSpace(tool.Get("type").String())
			switch toolType {
			case "", "function":
				appendDescriptor(tool, responsesToolName(tool), "", "", "function", source.priority, true)
			case "custom":
				if !isOpenAIResponsesApplyPatchCustomTool("custom", tool) {
					appendDescriptor(tool, responsesToolName(tool), "", "", "custom", source.priority, true)
				}
			case "namespace":
				appendNamespaceChildren(tool, source.priority)
			case "web_search":
				if externalWebAccess := tool.Get("external_web_access"); externalWebAccess.Exists() && !externalWebAccess.Bool() {
					return true
				}
				name := strings.TrimSpace(tool.Get("name").String())
				if name == "" {
					name = "web_search"
				}
				appendDescriptor(tool, name, "", "", "web_search", source.priority, true)
			default:
				if isUnsupportedOpenAIBuiltinToolType(toolType) {
					return true
				}
				appendDescriptor(tool, strings.TrimSpace(tool.Get("name").String()), "", "", toolType, source.priority, true)
			}
			return true
		})
	}
	return descriptors
}

func responsesToolDescriptorPrecedes(left, right responsesToolDescriptor) bool {
	// Keep top-level tools ahead of additional_tools, then let direct
	// declarations win over namespace children within the same source class.
	if left.sourcePriority != right.sourcePriority {
		return left.sourcePriority < right.sourcePriority
	}
	if left.direct != right.direct {
		return left.direct
	}
	return left.order < right.order
}

func responsesToolWinnersFromDescriptors(descriptors []responsesToolDescriptor) map[string]responsesToolDescriptor {
	winners := map[string]responsesToolDescriptor{}
	for _, descriptor := range descriptors {
		current, exists := winners[descriptor.name]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[descriptor.name] = descriptor
		}
	}
	return winners
}

func responsesToolWinners(root gjson.Result) map[string]responsesToolDescriptor {
	return responsesToolWinnersFromDescriptors(responsesToolDescriptors(root))
}

func responsesToolNameMapFromDescriptors(descriptors []responsesToolDescriptor, winners map[string]responsesToolDescriptor, acceptedToolNames map[string]struct{}) map[string]string {
	toolNameMap := map[string]string{}

	// Direct tool names are canonical aliases and must win over namespace
	// child aliases, regardless of declaration order.
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || !descriptor.direct {
			continue
		}
		if _, accepted := acceptedToolNames[descriptor.name]; !accepted {
			continue
		}
		toolNameMap[descriptor.name] = descriptor.name
	}

	// Namespace aliases fill only names that are not already owned by a
	// winning direct function/custom tool.
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || descriptor.direct || descriptor.childName == "" {
			continue
		}
		if _, accepted := acceptedToolNames[descriptor.name]; !accepted {
			continue
		}
		if _, exists := toolNameMap[descriptor.childName]; exists {
			continue
		}
		toolNameMap[descriptor.childName] = descriptor.name
	}
	return toolNameMap
}

func responsesToolNameMap(root gjson.Result, acceptedToolNames map[string]struct{}) map[string]string {
	descriptors := responsesToolDescriptors(root)
	return responsesToolNameMapFromDescriptors(descriptors, responsesToolWinnersFromDescriptors(descriptors), acceptedToolNames)
}

func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	names := make(map[string]struct{})
	root := gjson.ParseBytes(requestRawJSON)
	for name, descriptor := range responsesToolWinners(root) {
		if descriptor.toolType == "custom" {
			names[name] = struct{}{}
		}
	}
	return names
}

func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}

func sanitizeResponsesToolUseInput(name string, inputRaw []byte) []byte {
	if strings.TrimSpace(name) != "Read" {
		return inputRaw
	}
	pages := gjson.GetBytes(inputRaw, "pages")
	if pages.Type != gjson.String || pages.String() != "" {
		return inputRaw
	}
	cleaned, err := sjson.DeleteBytes(inputRaw, "pages")
	if err != nil {
		return inputRaw
	}
	return cleaned
}

func convertResponsesFunctionToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if d := responsesToolDescription(tool); d != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", d)
	}
	tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", normalizeClaudeToolInputSchema(responsesToolParameters(tool)))
	tJSON = common.AttachCacheControl(tJSON, tool)
	if !gjson.GetBytes(tJSON, "cache_control").Exists() {
		tJSON = common.AttachCacheControl(tJSON, tool.Get("function"))
	}
	return tJSON, true
}

func convertResponsesCustomToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if description := responsesToolDescription(tool); description != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", description)
	}
	tJSON = common.AttachCacheControl(tJSON, tool)
	return tJSON, true
}

func convertResponsesWebSearchToolToClaude(tool gjson.Result) ([]byte, bool) {
	if externalWebAccess := tool.Get("external_web_access"); externalWebAccess.Exists() && !externalWebAccess.Bool() {
		return nil, false
	}

	name := strings.TrimSpace(tool.Get("name").String())
	if name == "" {
		name = "web_search"
	}
	tJSON := []byte(`{"type":"web_search_20250305","name":""}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if maxUses := tool.Get("max_uses"); maxUses.Exists() {
		tJSON, _ = sjson.SetBytes(tJSON, "max_uses", maxUses.Int())
	}
	if allowedDomains := tool.Get("filters.allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "user_location", []byte(userLocation.Raw))
	}
	return tJSON, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

func normalizeClaudeToolInputSchema(parameters gjson.Result) []byte {
	raw := strings.TrimSpace(parameters.Raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return []byte(`{"type":"object","properties":{}}`)
	}
	result := gjson.Parse(raw)
	if !result.IsObject() {
		return []byte(`{"type":"object","properties":{}}`)
	}
	schema := []byte(raw)
	schemaType := result.Get("type").String()
	if schemaType == "" {
		schema, _ = sjson.SetBytes(schema, "type", "object")
		schemaType = "object"
	}
	if schemaType == "object" && !result.Get("properties").Exists() {
		schema, _ = sjson.SetRawBytes(schema, "properties", []byte(`{}`))
	}
	return schema
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func isUnsupportedOpenAIBuiltinToolType(toolType string) bool {
	switch toolType {
	case "image_generation", "file_search", "code_interpreter", "computer_use_preview":
		return true
	default:
		return false
	}
}

// isResponsesSystemLevelRole reports whether an input item carries system-level
// authority. The Responses API ranks developer and system instructions above
// user content, so both map to Claude's system slot rather than a user turn.
func isResponsesSystemLevelRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	default:
		return false
	}
}

// responsesSystemUnsupportedBlock represents a system-level content part that
// Claude cannot carry. Anthropic accepts text only in the top-level system field
// ("system.<i>.type: Input should be 'text'") and text, tool_addition and
// tool_removal in a role=system message, so images, files and unknown part types
// have no lossless mapping. The part is preserved as a typed marker instead of
// being dropped: silently discarding operator instructions is worse than a
// rejected request, and the marker lets the Claude executor fail the request with
// the offending type named. The original payload is not copied because the
// request can never succeed.
func responsesSystemUnsupportedBlock(part gjson.Result) []byte {
	partType := strings.TrimSpace(part.Get("type").String())
	if partType == "" {
		return nil
	}
	block := []byte(`{"type":""}`)
	block, _ = sjson.SetBytes(block, "type", partType)
	return block
}

// convertResponsesReasoningToClaudeThinking rebuilds one Claude thinking block
// from a Responses reasoning item so a replayed conversation keeps its chain of
// thought. Anthropic requires a signature on every thinking block and rejects an
// absent or empty one, so an item whose encrypted_content is missing or belongs
// to another provider is dropped rather than replayed as an unsigned block.
// Anthropic does not verify the text against the signature, which is what makes
// the summarized text safe to restore alongside it.
func convertResponsesReasoningToClaudeThinking(item gjson.Result) []byte {
	encrypted := item.Get("encrypted_content").String()
	if data, isRedacted := responsesRedactedThinkingData(encrypted); isRedacted {
		if data == "" {
			return nil
		}
		redactedPart := []byte(`{"type":"redacted_thinking","data":""}`)
		redactedPart, _ = sjson.SetBytes(redactedPart, "data", data)
		return redactedPart
	}

	signature, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, encrypted)
	if !ok {
		return nil
	}

	thinkingText := responsesReasoningText(item)
	thinkingPart := []byte(`{"type":"thinking","thinking":"","signature":""}`)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", thinkingText)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "signature", signature)
	return thinkingPart
}

// responsesRedactedThinkingData reports whether encrypted_content carries an
// Anthropic redacted_thinking payload and returns that payload.
func responsesRedactedThinkingData(encryptedContent string) (string, bool) {
	trimmed := strings.TrimSpace(encryptedContent)
	if !strings.HasPrefix(trimmed, ClaudeResponsesRedactedThinkingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, ClaudeResponsesRedactedThinkingPrefix)), true
}

// responsesReasoningText collects the reasoning text of a Responses item. OpenAI
// splits it across summary[] parts of type summary_text and content[] parts of
// type reasoning_text. Claude only ever produces summaries, but callers echo the
// item back through whichever array their SDK models, so both are read. content[]
// is only consulted when summary[] carried nothing, otherwise a client that
// mirrors the text into both arrays would replay it twice.
func responsesReasoningText(item gjson.Result) string {
	if text := responsesReasoningPartsText(item.Get("summary")); text != "" {
		return text
	}
	return responsesReasoningPartsText(item.Get("content"))
}

func responsesReasoningPartsText(parts gjson.Result) string {
	if !parts.Exists() || !parts.IsArray() {
		return ""
	}
	var builder strings.Builder
	parts.ForEach(func(_, part gjson.Result) bool {
		if text := part.Get("text"); text.Exists() {
			builder.WriteString(text.String())
		} else if part.Type == gjson.String {
			builder.WriteString(part.String())
		}
		return true
	})
	return builder.String()
}
