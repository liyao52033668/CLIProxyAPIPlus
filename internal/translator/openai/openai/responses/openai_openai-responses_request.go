package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI chat completions template with default values
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)

	// Set model name
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map Responses text format to Chat Completions response format.
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		if responseFormat := convertResponsesTextFormatToChatResponseFormat(textFormat); len(responseFormat) > 0 {
			out, _ = sjson.SetRawBytes(out, "response_format", responseFormat)
		}
	}

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		if maxTokens.Raw != "" {
			out, _ = sjson.SetRawBytes(out, "max_tokens", []byte(maxTokens.Raw))
		} else {
			out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Value())
		}
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	}

	// Map Responses reasoning effort to Chat Completions reasoning_effort so
	// downstream executors (and the shared thinking pipeline) see the caller's
	// thinking intensity instead of dropping it at the bridge.
	if effort := root.Get("reasoning.effort"); effort.Exists() {
		out, _ = sjson.SetBytes(out, "reasoning_effort", effort.String())
	}

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", systemMessage)
	}

	// Convert input array to messages. Chat completions bodies posted to the
	// responses endpoint carry their turns in "messages" instead; accept that
	// shape as a fallback so the turns are not silently dropped.
	input := root.Get("input")
	if !input.Exists() || !input.IsArray() {
		input = root.Get("messages")
	}
	if input.IsArray() {
		inputItems := input.Array()
		outputCallIDs := make(map[string]struct{})
		for _, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				continue
			}
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				continue
			}
			outputCallIDs[callID] = struct{}{}
		}

		pendingToolCalls := make([]interface{}, 0)
		pendingToolCallIDs := make([]string, 0)
		pendingReasoningContent := ""
		latestReasoningContent := ""
		awaitingToolOutputs := make(map[string]struct{})
		deferredMessages := make([][]byte, 0)

		// Reasoning enabled in the session makes tool-call turns eligible for a
		// placeholder when no concrete reasoning content was observed.
		hasReasoningInSession := false
		if effortRes := root.Get("reasoning.effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if effortRes := root.Get("reasoning_effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if reasoningObj := root.Get("reasoning"); reasoningObj.Exists() {
			reasoningRaw := strings.ToLower(strings.TrimSpace(reasoningObj.String()))
			if reasoningRaw != "" && reasoningRaw != "none" && reasoningRaw != "false" && reasoningRaw != "{}" {
				hasReasoningInSession = true
			}
		}
		if !hasReasoningInSession {
			for _, item := range inputItems {
				itemType := item.Get("type").String()
				if itemType == "reasoning" || item.Get("reasoning_content").Exists() {
					hasReasoningInSession = true
					break
				}
			}
		}

		fallbackToolReasoning := func() string {
			if latestReasoningContent != "" {
				return latestReasoningContent
			}
			if hasReasoningInSession {
				return "[reasoning unavailable]"
			}
			return ""
		}

		takePendingReasoningContent := func() string {
			reasoningContent := pendingReasoningContent
			pendingReasoningContent = ""
			return reasoningContent
		}
		flushPendingToolCalls := func() {
			if len(pendingToolCalls) == 0 {
				return
			}
			assistantMessage := []byte(`{"role":"assistant","tool_calls":[]}`)
			assistantMessage, _ = sjson.SetBytes(assistantMessage, "tool_calls", pendingToolCalls)
			reasoningContent := takePendingReasoningContent()
			if reasoningContent != "" {
				assistantMessage, _ = sjson.SetBytes(assistantMessage, "reasoning_content", reasoningContent)
				if isUsableResponsesReasoning(reasoningContent) {
					latestReasoningContent = reasoningContent
				}
			} else if fallback := fallbackToolReasoning(); fallback != "" {
				assistantMessage, _ = sjson.SetBytes(assistantMessage, "reasoning_content", fallback)
			}
			out, _ = sjson.SetRawBytes(out, "messages.-1", assistantMessage)
			for _, id := range pendingToolCallIDs {
				if strings.TrimSpace(id) == "" {
					continue
				}
				awaitingToolOutputs[id] = struct{}{}
			}
			pendingToolCalls = pendingToolCalls[:0]
			pendingToolCallIDs = pendingToolCallIDs[:0]
		}
		flushDeferredMessages := func() {
			for _, message := range deferredMessages {
				out, _ = sjson.SetRawBytes(out, "messages.-1", message)
			}
			deferredMessages = deferredMessages[:0]
		}
		hasAwaitingToolOutput := func() bool {
			for id := range awaitingToolOutputs {
				if _, ok := outputCallIDs[id]; ok {
					return true
				}
			}
			return false
		}
		appendRegularMessage := func(message []byte) {
			// Keep tool-call adjacency strict for providers that require
			// assistant(tool_calls) -> tool(tool_call_id) with no message in between.
			if hasAwaitingToolOutput() {
				deferredMessages = append(deferredMessages, message)
				return
			}
			out, _ = sjson.SetRawBytes(out, "messages.-1", message)
		}
		appendPendingReasoningMessage := func() {
			reasoningContent := takePendingReasoningContent()
			if reasoningContent == "" {
				return
			}
			if isUsableResponsesReasoning(reasoningContent) {
				latestReasoningContent = reasoningContent
			}
			message := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
			message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
			appendRegularMessage(message)
		}

		for _, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}
			if itemType != "function_call" && itemType != "custom_tool_call" {
				flushPendingToolCalls()
			}

			switch itemType {
			case "message", "":
				// Handle regular message conversion
				role := item.Get("role").String()
				if role == "developer" {
					role = "user"
				}
				if role != "assistant" {
					appendPendingReasoningMessage()
					latestReasoningContent = ""
				}
				message := []byte(`{"role":"","content":[]}`)
				message, _ = sjson.SetBytes(message, "role", role)

				if content := item.Get("content"); content.Exists() && content.IsArray() {
					var messageContent string
					var toolCalls []any

					content.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", text)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
							message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
						}
						return true
					})

					if messageContent != "" {
						message, _ = sjson.SetBytes(message, "content", messageContent)
					}

					if len(toolCalls) > 0 {
						message, _ = sjson.SetBytes(message, "tool_calls", toolCalls)
					}
				} else if content.Type == gjson.String {
					message, _ = sjson.SetBytes(message, "content", content.String())
				}

				if role == "assistant" {
					reasoningContent := item.Get("reasoning_content").String()
					if reasoningContent == "" {
						reasoningContent = takePendingReasoningContent()
					} else {
						pendingReasoningContent = ""
					}
					if reasoningContent != "" {
						message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
						if isUsableResponsesReasoning(reasoningContent) {
							latestReasoningContent = reasoningContent
						}
					}
				}

				appendRegularMessage(message)

			case "reasoning":
				reasoningContent := collectOpenAIResponsesReasoningContent(item)
				if pendingReasoningContent == "" {
					pendingReasoningContent = reasoningContent
				} else {
					pendingReasoningContent += reasoningContent
				}
				if isUsableResponsesReasoning(reasoningContent) {
					latestReasoningContent = reasoningContent
				}

			case "function_call":
				rc := item.Get("reasoning_content").String()
				if rc != "" {
					if pendingReasoningContent == "" {
						pendingReasoningContent = rc
					} else {
						pendingReasoningContent += rc
					}
				}
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// Buffer consecutive function calls and emit them as one assistant message.
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callId := item.Get("call_id"); callId.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "id", callId.String())
				}

				if name := item.Get("name"); name.Exists() {
					functionName := name.String()
					if namespace := strings.TrimSpace(item.Get("namespace").String()); namespace != "" {
						functionName = qualifyResponsesNamespaceToolName(namespace, functionName)
					} else {
						functionName = canonicalResponsesToolName(inputRawJSON, functionName)
					}
					toolCall, _ = sjson.SetBytes(toolCall, "function.name", functionName)
				}

				if arguments := item.Get("arguments"); arguments.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
				}
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "function_call_output":
				callID := strings.TrimSpace(item.Get("call_id").String())
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					// Orphan outputs (empty call_id or no matching assistant
					// tool_calls, e.g. Codex send_message_to_thread cards) must
					// not become tool messages. Emit as user text instead.
					appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setFunctionCallOutputContent, appendRegularMessage)
				} else {
					toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						toolMessage = setFunctionCallOutputContent(toolMessage, output)
					}
					out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
				}
				if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
					flushDeferredMessages()
				}

			case "custom_tool_call":
				rc := item.Get("reasoning_content").String()
				if rc != "" {
					if pendingReasoningContent == "" {
						pendingReasoningContent = rc
					} else {
						pendingReasoningContent += rc
					}
				}
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// Codex freeform tool call replay: wrap the raw input so it
				// matches the {"input": string} function shape used when
				// converting custom tool definitions.
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				toolCall, _ = sjson.SetBytes(toolCall, "id", item.Get("call_id").String())
				functionName := item.Get("name").String()
				if namespace := item.Get("namespace").String(); namespace != "" {
					functionName = qualifyResponsesNamespaceToolName(namespace, functionName)
				} else {
					functionName = canonicalResponsesToolName(inputRawJSON, functionName)
				}
				toolCall, _ = sjson.SetBytes(toolCall, "function.name", functionName)
				wrappedArgs, _ := sjson.SetBytes([]byte(`{"input":""}`), "input", item.Get("input").String())
				toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", string(wrappedArgs))
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "custom_tool_call_output":
				callID := strings.TrimSpace(item.Get("call_id").String())
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setCustomToolCallOutputContent, appendRegularMessage)
				} else {
					toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						toolMessage = setCustomToolCallOutputContent(toolMessage, output)
					}
					out, _ = sjson.SetRawBytes(out, "messages.-1", toolMessage)
				}
				if len(awaitingToolOutputs) == 0 && len(deferredMessages) > 0 {
					flushDeferredMessages()
				}
			}

		}
		flushPendingToolCalls()
		appendPendingReasoningMessage()
		flushDeferredMessages()
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	var chatCompletionsTools []any
	appendChatTools := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			for _, chatTool := range convertResponsesToolToOpenAIChatTools(tool) {
				chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())
			}
			return true
		})
	}
	appendChatTools(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendChatTools(item.Get("tools"))
			}
			return true
		})
	}
	if len(chatCompletionsTools) > 0 {
		out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
		}
	}

	// Convert tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", convertResponsesToolChoiceToChatCompletions(toolChoice, inputRawJSON))
	}

	return out
}

func convertResponsesToolChoiceToChatCompletions(toolChoice gjson.Result, inputRawJSON []byte) []byte {
	if !toolChoice.IsObject() {
		return []byte(toolChoice.Raw)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType != "function" && choiceType != "custom" {
		return []byte(toolChoice.Raw)
	}

	name := toolChoice.Get("function.name").String()
	if name == "" {
		name = toolChoice.Get("custom.name").String()
	}
	if name == "" {
		name = toolChoice.Get("name").String()
	}
	if name == "" {
		return []byte(toolChoice.Raw)
	}

	namespace := strings.TrimSpace(toolChoice.Get("namespace").String())
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("function.namespace").String())
	}
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("custom.namespace").String())
	}
	if namespace != "" {
		name = qualifyResponsesNamespaceToolName(namespace, name)
	} else {
		name = canonicalResponsesToolName(inputRawJSON, name)
	}

	converted := []byte(`{"type":"function","function":{"name":""}}`)
	converted, _ = sjson.SetBytes(converted, "function.name", name)
	return converted
}

func appendStandaloneResponsesToolOutputAsUser(output gjson.Result, setContent func([]byte, gjson.Result) []byte, appendMessage func([]byte)) {
	userMessage := []byte(`{"role":"user","content":""}`)
	if output.Exists() {
		userMessage = setContent(userMessage, output)
	}
	content := gjson.GetBytes(userMessage, "content")
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String && strings.TrimSpace(content.String()) == "" {
		return
	}
	if content.IsArray() && !content.Get("0").Exists() {
		return
	}
	appendMessage(userMessage)
}

func setFunctionCallOutputContent(toolMessage []byte, output gjson.Result) []byte {
	return setToolMessageTextContent(toolMessage, responsesToolOutputText(output))
}

func setCustomToolCallOutputContent(toolMessage []byte, output gjson.Result) []byte {
	structured := output
	if output.Type == gjson.String && gjson.Valid(output.String()) {
		structured = gjson.Parse(output.String())
	}

	if structured.IsArray() {
		content := []byte(`{"arr":[]}`)
		hasImage := false
		structured.ForEach(func(_, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "input_text":
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
				content, _ = sjson.SetRawBytes(content, "arr.-1", textPart)
			case "input_image":
				imageURL := strings.TrimSpace(part.Get("image_url").String())
				if imageURL == "" {
					return true
				}
				detail := part.Get("detail").String()
				if detail == "original" {
					detail = "high"
				}
				imagePart := []byte(`{"type":"image_url","image_url":{"url":"","detail":""}}`)
				imagePart, _ = sjson.SetBytes(imagePart, "image_url.url", imageURL)
				if detail != "" {
					imagePart, _ = sjson.SetBytes(imagePart, "image_url.detail", detail)
				}
				content, _ = sjson.SetRawBytes(content, "arr.-1", imagePart)
				hasImage = true
			}
			return true
		})
		if hasImage {
			return setRawToolMessageContent(toolMessage, gjson.GetBytes(content, "arr"))
		}
	}

	return setToolMessageTextContent(toolMessage, responsesToolOutputText(output))
}

func setRawToolMessageContent(toolMessage []byte, content gjson.Result) []byte {
	toolMessage, _ = sjson.SetRawBytes(toolMessage, "content", []byte(content.Raw))
	return toolMessage
}

func setToolMessageTextContent(toolMessage []byte, content string) []byte {
	toolMessage, _ = sjson.SetBytes(toolMessage, "content", content)
	return toolMessage
}

func collectOpenAIResponsesReasoningContent(item gjson.Result) string {
	var reasoningText strings.Builder
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, summaryItem gjson.Result) bool {
			if summaryItem.Get("type").String() != "summary_text" {
				return true
			}
			reasoningText.WriteString(summaryItem.Get("text").String())
			return true
		})
	}
	if reasoningText.Len() == 0 {
		return "[reasoning unavailable]"
	}
	return reasoningText.String()
}

func convertResponsesTextFormatToChatResponseFormat(textFormat gjson.Result) []byte {
	formatType := textFormat.Get("type").String()
	switch formatType {
	case "text", "json_object":
		responseFormat := []byte(`{"type":""}`)
		responseFormat, _ = sjson.SetBytes(responseFormat, "type", formatType)
		return responseFormat
	case "json_schema":
		responseFormat := []byte(`{"type":"json_schema","json_schema":{}}`)
		for _, field := range []string{"name", "description", "strict"} {
			if value := textFormat.Get(field); value.Exists() {
				responseFormat, _ = sjson.SetBytes(responseFormat, "json_schema."+field, value.Value())
			}
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			responseFormat, _ = sjson.SetRawBytes(responseFormat, "json_schema.schema", []byte(schema.Raw))
		}
		return responseFormat
	default:
		return nil
	}
}

func isUsableResponsesReasoning(reasoning string) bool {
	trimmed := strings.TrimSpace(reasoning)
	return trimmed != "" && trimmed != "[reasoning unavailable]"
}
