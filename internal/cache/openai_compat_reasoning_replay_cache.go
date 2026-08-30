package cache

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// OpenAICompatReasoningReplayCacheTTL limits how long captured assistant
	// reasoning stays replayable for DeepSeek-style thinking-mode providers.
	OpenAICompatReasoningReplayCacheTTL = 1 * time.Hour

	// OpenAICompatReasoningReplayCacheMaxEntries bounds process memory used for
	// replay continuity. Oldest entries are evicted first.
	OpenAICompatReasoningReplayCacheMaxEntries = 10240

	// OpenAICompatReasoningReplayCacheEvictBatchSize leaves headroom after the
	// cache reaches capacity so high write volume does not rescan the map every turn.
	OpenAICompatReasoningReplayCacheEvictBatchSize = 128

	// OpenAICompatReasoningReplayCacheMaxBytesPerEntry bounds one reasoning value.
	OpenAICompatReasoningReplayCacheMaxBytesPerEntry = 1 << 20
)

type openAICompatReasoningReplayEntry struct {
	Reasoning string
	Timestamp time.Time
}

var (
	openAICompatReasoningReplayMu      sync.Mutex
	openAICompatReasoningReplayEntries = make(map[string]openAICompatReasoningReplayEntry)
)

// StoreOpenAICompatReasoningTurn caches one assistant turn's reasoning for
// later replay. One entry is written per tool-call ID, one for the message
// content fingerprint, and one caller-scoped latest entry.
func StoreOpenAICompatReasoningTurn(callerKey, reasoning string, toolCallIDs []string, contentFingerprint string) {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" || len(reasoning) > OpenAICompatReasoningReplayCacheMaxBytesPerEntry {
		return
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	openAICompatReasoningReplayMu.Lock()
	defer openAICompatReasoningReplayMu.Unlock()
	storeOpenAICompatReasoningEntryLocked(openAICompatReasoningReplayLatestKey(callerKey), reasoning, now)
	for _, toolCallID := range toolCallIDs {
		toolCallID = strings.TrimSpace(toolCallID)
		if toolCallID == "" {
			continue
		}
		storeOpenAICompatReasoningEntryLocked(openAICompatReasoningReplayToolCallKey(callerKey, toolCallID), reasoning, now)
	}
	contentFingerprint = strings.TrimSpace(contentFingerprint)
	if contentFingerprint != "" {
		storeOpenAICompatReasoningEntryLocked(openAICompatReasoningReplayContentKey(callerKey, contentFingerprint), reasoning, now)
	}
}

// GetOpenAICompatReasoningForToolCall retrieves the reasoning captured for one
// tool-call ID.
func GetOpenAICompatReasoningForToolCall(callerKey, toolCallID string) (string, bool) {
	return getOpenAICompatReasoningReplay(openAICompatReasoningReplayToolCallKey(callerKey, strings.TrimSpace(toolCallID)))
}

// GetOpenAICompatReasoningForContent retrieves the reasoning captured for one
// assistant message content fingerprint.
func GetOpenAICompatReasoningForContent(callerKey, contentFingerprint string) (string, bool) {
	return getOpenAICompatReasoningReplay(openAICompatReasoningReplayContentKey(callerKey, strings.TrimSpace(contentFingerprint)))
}

// GetOpenAICompatLatestReasoning retrieves the most recent reasoning captured
// for a caller.
func GetOpenAICompatLatestReasoning(callerKey string) (string, bool) {
	return getOpenAICompatReasoningReplay(openAICompatReasoningReplayLatestKey(callerKey))
}

// openAICompatReasoningReplayCallerScope maps empty caller keys to a shared
// scope so keyless deployments still get replay continuity.
func openAICompatReasoningReplayCallerScope(callerKey string) string {
	callerKey = strings.TrimSpace(callerKey)
	if callerKey == "" {
		return "default"
	}
	return callerKey
}

func storeOpenAICompatReasoningEntryLocked(key, reasoning string, now time.Time) {
	if key == "" {
		return
	}
	openAICompatReasoningReplayEntries[key] = openAICompatReasoningReplayEntry{Reasoning: reasoning, Timestamp: now}
	if len(openAICompatReasoningReplayEntries) > OpenAICompatReasoningReplayCacheMaxEntries {
		evictOldestOpenAICompatReasoningReplayEntriesLocked(OpenAICompatReasoningReplayCacheEvictBatchSize)
	}
}

func getOpenAICompatReasoningReplay(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	openAICompatReasoningReplayMu.Lock()
	defer openAICompatReasoningReplayMu.Unlock()
	entry, ok := openAICompatReasoningReplayEntries[key]
	if !ok {
		return "", false
	}
	if now.Sub(entry.Timestamp) > OpenAICompatReasoningReplayCacheTTL {
		delete(openAICompatReasoningReplayEntries, key)
		return "", false
	}
	entry.Timestamp = now
	openAICompatReasoningReplayEntries[key] = entry
	return entry.Reasoning, true
}

func openAICompatReasoningReplayToolCallKey(callerKey, toolCallID string) string {
	callerKey = openAICompatReasoningReplayCallerScope(callerKey)
	if callerKey == "" || toolCallID == "" {
		return ""
	}
	return "openai-reasoning-replay\x00" + callerKey + "\x00tool\x00" + toolCallID
}

func openAICompatReasoningReplayContentKey(callerKey, contentFingerprint string) string {
	callerKey = openAICompatReasoningReplayCallerScope(callerKey)
	if callerKey == "" || contentFingerprint == "" {
		return ""
	}
	return "openai-reasoning-replay\x00" + callerKey + "\x00turn\x00" + contentFingerprint
}

func openAICompatReasoningReplayLatestKey(callerKey string) string {
	callerKey = openAICompatReasoningReplayCallerScope(callerKey)
	if callerKey == "" {
		return ""
	}
	return "openai-reasoning-replay\x00" + callerKey + "\x00latest"
}

func evictOldestOpenAICompatReasoningReplayEntriesLocked(count int) {
	if count <= 0 || len(openAICompatReasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(openAICompatReasoningReplayEntries))
	for key, entry := range openAICompatReasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		delete(openAICompatReasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredOpenAICompatReasoningReplayCache(now time.Time) {
	openAICompatReasoningReplayMu.Lock()
	for key, entry := range openAICompatReasoningReplayEntries {
		if now.Sub(entry.Timestamp) > OpenAICompatReasoningReplayCacheTTL {
			delete(openAICompatReasoningReplayEntries, key)
		}
	}
	openAICompatReasoningReplayMu.Unlock()
}
