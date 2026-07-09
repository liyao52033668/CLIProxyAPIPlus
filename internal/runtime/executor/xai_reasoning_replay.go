package executor

import (
	"context"
	"fmt"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

var getXAIReasoningReplayItemsRequired = internalcache.GetXAIReasoningReplayItemsRequired

type xaiReasoningReplayScope struct {
	modelName  string
	sessionKey string
}

func (s xaiReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func applyXAIReasoningReplayCacheRequired(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, xaiReasoningReplayScope, error) {
	scope := xaiReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	if !scope.valid() || !xaiReasoningReplayEnabledForSource(from) {
		return body, scope, nil
	}
	if xaiInputHasValidReasoningEncryptedContent(body) {
		return body, scope, nil
	}

	items, ok, err := getXAIReasoningReplayItemsRequired(ctx, scope.modelName, scope.sessionKey)
	if err != nil || !ok || len(items) == 0 {
		// Best-effort: cache outages should not fail the request path.
		return body, scope, nil
	}
	items = filterXAIReasoningReplayItemsForInput(body, items)
	if len(items) == 0 {
		return body, scope, nil
	}
	inputItems := gjson.GetBytes(body, "input").Array()
	insertAt := claudeReplayInsertIndex(inputItems)
	updated, errInsert := insertReplayItems(body, items, insertAt)
	if errInsert != nil {
		return body, scope, nil
	}
	return updated, scope, nil
}

func xaiReasoningReplayScopeFromRequest(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) xaiReasoningReplayScope {
	// Reuse Codex/Claude session key extraction so Claude clients get continuity.
	codexScope := codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	sessionKey := codexScope.sessionKey
	if sessionKey == "" && opts.Metadata != nil {
		if raw, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]; ok {
			if value := strings.TrimSpace(fmt.Sprint(raw)); value != "" && value != "<nil>" {
				sessionKey = value
			}
		}
	}
	return xaiReasoningReplayScope{modelName: codexScope.modelName, sessionKey: sessionKey}
}

func xaiReasoningReplayEnabledForSource(from sdktranslator.Format) bool {
	return strings.EqualFold(from.String(), "claude")
}

func xaiInputHasValidReasoningEncryptedContent(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		encrypted := item.Get("encrypted_content")
		if encrypted.Exists() && encrypted.Type == gjson.String && strings.TrimSpace(encrypted.String()) != "" {
			return true
		}
	}
	return false
}

func filterXAIReasoningReplayItemsForInput(body []byte, items [][]byte) [][]byte {
	if len(items) == 0 {
		return nil
	}
	// Prefer replaying function_call items that match tool outputs already present
	// in the current input (Claude tool_result -> function_call_output path).
	outputCallIDs := map[string]struct{}{}
	existingCallIDs := map[string]struct{}{}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		itemType := item.Get("type").String()
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			continue
		}
		switch itemType {
		case "function_call_output":
			outputCallIDs[callID] = struct{}{}
		case "function_call", "custom_tool_call":
			existingCallIDs[callID] = struct{}{}
		}
	}

	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemType := gjson.GetBytes(item, "type").String()
		switch itemType {
		case "reasoning":
			filtered = append(filtered, item)
		case "function_call", "custom_tool_call":
			callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
			if callID != "" {
				if _, exists := existingCallIDs[callID]; exists {
					continue
				}
				// If the current turn already has tool outputs, only replay the
				// matching tool calls that produced those outputs.
				if len(outputCallIDs) > 0 {
					if _, ok := outputCallIDs[callID]; !ok {
						continue
					}
				}
			}
			filtered = append(filtered, item)
		default:
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func cacheXAIReasoningReplayFromCompleted(ctx context.Context, scope xaiReasoningReplayScope, completedData []byte) {
	if !scope.valid() {
		return
	}
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() {
		return
	}
	items := make([][]byte, 0, len(output.Array()))
	for _, item := range output.Array() {
		if item.Type == gjson.JSON {
			items = append(items, []byte(item.Raw))
		}
	}
	_ = ctx
	internalcache.CacheXAIReasoningReplayItems(scope.modelName, scope.sessionKey, items)
}
