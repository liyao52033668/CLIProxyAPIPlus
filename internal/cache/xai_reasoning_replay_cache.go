package cache

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// XAIReasoningReplayCacheTTL limits how long encrypted reasoning replay
	// items stay in process memory.
	XAIReasoningReplayCacheTTL = 1 * time.Hour

	// XAIReasoningReplayCacheMaxEntries bounds process memory for replay
	// continuity. Oldest entries are evicted first.
	XAIReasoningReplayCacheMaxEntries = 10240

	// XAIReasoningReplayCacheEvictBatchSize leaves headroom after the cache
	// reaches capacity so high write volume does not rescan the map every turn.
	XAIReasoningReplayCacheEvictBatchSize = 128
)

type xaiReasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
}

var (
	xaiReasoningReplayMu      sync.Mutex
	xaiReasoningReplayEntries = make(map[string]xaiReasoningReplayEntry)
)

// CacheXAIReasoningReplayItem stores a final Grok reasoning item for stateless
// replay. The stored item is normalized to the minimal shape accepted by
// Responses input replay.
func CacheXAIReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheXAIReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheXAIReasoningReplayItems stores the final Grok assistant output items
// needed to replay a stateless next turn.
func CacheXAIReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	return CacheXAIReasoningReplayItemsBestEffort(context.Background(), modelName, sessionKey, items)
}

// XAIReasoningReplayStoreStatus reports why a completed-turn cache write
// succeeded or failed so callers can decide whether to keep prior entries.
type XAIReasoningReplayStoreStatus int

const (
	// XAIReasoningReplayStoreInvalidArgs means model/session were empty.
	XAIReasoningReplayStoreInvalidArgs XAIReasoningReplayStoreStatus = iota
	// XAIReasoningReplayStored means a valid reasoning batch was written.
	XAIReasoningReplayStored
	// XAIReasoningReplayNoReplayableState means the completed output had no
	// cacheable reasoning batch (for example reasoning disabled).
	XAIReasoningReplayNoReplayableState
	// XAIReasoningReplayStoreBackendError means normalize succeeded but the
	// storage backend failed; previous entries should be retained.
	XAIReasoningReplayStoreBackendError
)

// CacheXAIReasoningReplayItemsBestEffort stores replay items for completed response paths.
func CacheXAIReasoningReplayItemsBestEffort(ctx context.Context, modelName, sessionKey string, items [][]byte) bool {
	return StoreXAIReasoningReplayItems(ctx, modelName, sessionKey, items) == XAIReasoningReplayStored
}

// StoreXAIReasoningReplayItems stores replay items and distinguishes empty
// completed state from backend failures.
func StoreXAIReasoningReplayItems(ctx context.Context, modelName, sessionKey string, items [][]byte) XAIReasoningReplayStoreStatus {
	_ = ctx
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return XAIReasoningReplayStoreInvalidArgs
	}
	normalized, ok := normalizeXAIReasoningReplayItems(items)
	if !ok {
		return XAIReasoningReplayNoReplayableState
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	xaiReasoningReplayMu.Lock()
	defer xaiReasoningReplayMu.Unlock()
	xaiReasoningReplayEntries[key] = xaiReasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
	}
	if len(xaiReasoningReplayEntries) > XAIReasoningReplayCacheMaxEntries {
		evictOldestXAIReasoningReplayEntriesLocked(XAIReasoningReplayCacheEvictBatchSize)
	}
	return XAIReasoningReplayStored
}

// GetXAIReasoningReplayItem retrieves a normalized reasoning replay item.
func GetXAIReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetXAIReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetXAIReasoningReplayItems retrieves normalized assistant output items.
func GetXAIReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	items, ok, err := GetXAIReasoningReplayItemsRequired(context.Background(), modelName, sessionKey)
	if err != nil {
		return nil, false
	}
	return items, ok
}

// GetXAIReasoningReplayItemsRequired retrieves normalized assistant output items.
// Local builds use process memory only.
func GetXAIReasoningReplayItemsRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, bool, error) {
	_ = ctx
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, false, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	xaiReasoningReplayMu.Lock()
	defer xaiReasoningReplayMu.Unlock()
	entry, ok := xaiReasoningReplayEntries[key]
	if !ok {
		return nil, false, nil
	}
	if now.Sub(entry.Timestamp) > XAIReasoningReplayCacheTTL {
		delete(xaiReasoningReplayEntries, key)
		return nil, false, nil
	}
	entry.Timestamp = now
	xaiReasoningReplayEntries[key] = entry
	return cloneXAIReasoningReplayItems(entry.Items), true, nil
}

// DeleteXAIReasoningReplayItem removes one replay item after upstream rejects
// it or the caller otherwise knows it is stale.
func DeleteXAIReasoningReplayItem(modelName, sessionKey string) {
	_ = DeleteXAIReasoningReplayItemRequired(context.Background(), modelName, sessionKey)
}

// DeleteXAIReasoningReplayItemRequired removes one replay item.
func DeleteXAIReasoningReplayItemRequired(ctx context.Context, modelName, sessionKey string) error {
	_ = ctx
	key := xaiReasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil
	}
	xaiReasoningReplayMu.Lock()
	delete(xaiReasoningReplayEntries, key)
	xaiReasoningReplayMu.Unlock()
	return nil
}

// ClearXAIReasoningReplayCache clears all xAI reasoning replay state.
func ClearXAIReasoningReplayCache() {
	xaiReasoningReplayMu.Lock()
	xaiReasoningReplayEntries = make(map[string]xaiReasoningReplayEntry)
	xaiReasoningReplayMu.Unlock()
}

func xaiReasoningReplayCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	return modelName + "\x00" + sessionKey
}

func normalizeXAIReasoningReplayItems(items [][]byte) ([][]byte, bool) {
	normalized := make([][]byte, 0, len(items))
	hasReplayAnchor := false
	for _, item := range items {
		normalizedItem, ok := normalizeXAIReasoningReplayItem(item)
		if ok {
			normalized = append(normalized, normalizedItem)
			switch strings.TrimSpace(gjson.GetBytes(normalizedItem, "type").String()) {
			case "reasoning", "function_call", "custom_tool_call":
				hasReplayAnchor = true
			}
		}
	}
	return normalized, hasReplayAnchor
}

func normalizeXAIReasoningReplayItem(item []byte) ([]byte, bool) {
	itemResult := gjson.ParseBytes(item)
	switch strings.TrimSpace(itemResult.Get("type").String()) {
	case "reasoning":
		return normalizeXAIReasoningReplayReasoningItem(itemResult)
	case "message":
		return normalizeXAIReasoningReplayMessageItem(itemResult)
	case "function_call":
		return normalizeXAIReasoningReplayFunctionCallItem(itemResult)
	case "custom_tool_call":
		return normalizeXAIReasoningReplayCustomToolCallItem(itemResult)
	default:
		return nil, false
	}
}

func normalizeXAIReasoningReplayReasoningItem(itemResult gjson.Result) ([]byte, bool) {
	encryptedContentResult := itemResult.Get("encrypted_content")
	if encryptedContentResult.Type != gjson.String {
		return nil, false
	}
	encryptedContent := encryptedContentResult.String()
	if encryptedContent != strings.TrimSpace(encryptedContent) {
		return nil, false
	}
	if _, err := signature.InspectGrokEncryptedContent(encryptedContent); err != nil {
		return nil, false
	}

	normalized := []byte(`{"type":"reasoning","summary":[],"content":null}`)
	normalized, _ = sjson.SetBytes(normalized, "encrypted_content", encryptedContent)
	return normalized, true
}

func normalizeXAIReasoningReplayMessageItem(itemResult gjson.Result) ([]byte, bool) {
	if !strings.EqualFold(strings.TrimSpace(itemResult.Get("role").String()), "assistant") {
		return nil, false
	}
	content := itemResult.Get("content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return nil, false
	}

	normalized := []byte(`{"type":"message","role":"assistant","content":[]}`)
	for _, part := range content.Array() {
		partType := strings.TrimSpace(part.Get("type").String())
		var nextPart []byte
		switch partType {
		case "output_text":
			textValue := part.Get("text")
			if textValue.Type != gjson.String {
				continue
			}
			nextPart = []byte(`{"type":"output_text","text":""}`)
			nextPart, _ = sjson.SetBytes(nextPart, "text", textValue.String())
		case "refusal":
			// Responses API refusal parts use the "refusal" field, not "text".
			refusalValue := part.Get("refusal")
			if refusalValue.Type != gjson.String {
				continue
			}
			nextPart = []byte(`{"type":"refusal","refusal":""}`)
			nextPart, _ = sjson.SetBytes(nextPart, "refusal", refusalValue.String())
		default:
			continue
		}
		updated, errSet := sjson.SetRawBytes(normalized, "content.-1", nextPart)
		if errSet != nil {
			return nil, false
		}
		normalized = updated
	}
	if len(gjson.GetBytes(normalized, "content").Array()) == 0 {
		return nil, false
	}
	return normalized, true
}

func normalizeXAIReasoningReplayFunctionCallItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	name := strings.TrimSpace(itemResult.Get("name").String())
	arguments := itemResult.Get("arguments")
	if callID == "" || name == "" || arguments.Type != gjson.String {
		return nil, false
	}

	normalized := []byte(`{"type":"function_call"}`)
	normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	normalized, _ = sjson.SetBytes(normalized, "arguments", arguments.String())
	return normalized, true
}

func normalizeXAIReasoningReplayCustomToolCallItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	name := strings.TrimSpace(itemResult.Get("name").String())
	input := itemResult.Get("input")
	if callID == "" || name == "" || !input.Exists() {
		return nil, false
	}

	normalized := []byte(`{"type":"custom_tool_call","status":"completed"}`)
	if status := strings.TrimSpace(itemResult.Get("status").String()); status != "" {
		normalized, _ = sjson.SetBytes(normalized, "status", status)
	}
	normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	if input.Type == gjson.String {
		normalized, _ = sjson.SetBytes(normalized, "input", input.String())
	} else {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte(input.Raw))
	}
	return normalized, true
}

func cloneXAIReasoningReplayItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestXAIReasoningReplayEntriesLocked(count int) {
	if count <= 0 || len(xaiReasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(xaiReasoningReplayEntries))
	for key, entry := range xaiReasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		delete(xaiReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredXAIReasoningReplayCache(now time.Time) {
	xaiReasoningReplayMu.Lock()
	for key, entry := range xaiReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > XAIReasoningReplayCacheTTL {
			delete(xaiReasoningReplayEntries, key)
		}
	}
	xaiReasoningReplayMu.Unlock()
}
