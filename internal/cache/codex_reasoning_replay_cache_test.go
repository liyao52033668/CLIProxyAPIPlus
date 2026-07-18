package cache

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func validCodexReasoningReplayEncryptedContentForTest(seed byte) string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestCodexReasoningReplayCacheRejectsInvalidItems(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	if CacheCodexReasoningReplayItem("gpt-5.4", "session", []byte(`{"type":"reasoning","encrypted_content":"bad","summary":[]}`)) {
		t.Fatal("invalid encrypted_content should not be cached")
	}
	if _, ok := GetCodexReasoningReplayItem("gpt-5.4", "session"); ok {
		t.Fatal("invalid item was cached")
	}
}

func TestCodexReasoningReplayCacheScopesByModelAndSession(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningReplayEncryptedContentForTest(7)
	if !CacheCodexReasoningReplayItem("gpt-5.4", "session-a", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}`)) {
		t.Fatal("valid item was not cached")
	}

	if _, ok := GetCodexReasoningReplayItem("gpt-5.5", "session-a"); ok {
		t.Fatal("cache should not hit across models")
	}
	if _, ok := GetCodexReasoningReplayItem("gpt-5.4", "session-b"); ok {
		t.Fatal("cache should not hit across sessions")
	}

	item, ok := GetCodexReasoningReplayItem("gpt-5.4", "session-a")
	if !ok {
		t.Fatal("cache miss for original model and session")
	}
	if string(item) != `{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}` {
		t.Fatalf("normalized item = %s", string(item))
	}
}

func TestCodexReasoningReplayCacheBatchEvictsWhenFull(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningReplayEncryptedContentForTest(9)
	item := []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + encryptedContent + `"}`)
	for i := 0; i <= CodexReasoningReplayCacheMaxEntries; i++ {
		if !CacheCodexReasoningReplayItem("gpt-5.4", fmt.Sprintf("session-%d", i), item) {
			t.Fatalf("cache insert %d failed", i)
		}
	}

	codexReasoningReplayMu.Lock()
	gotLen := len(codexReasoningReplayEntries)
	codexReasoningReplayMu.Unlock()
	if gotLen >= CodexReasoningReplayCacheMaxEntries {
		t.Fatalf("cache entries = %d, want batch eviction below max %d", gotLen, CodexReasoningReplayCacheMaxEntries)
	}
}

func TestCodexReasoningReplayCacheEvictsOldestEntryOverTotalByteBudget(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	oldKey := codexReasoningReplayCacheKey("gpt-5.4", "old-session")
	codexReasoningReplayMu.Lock()
	codexReasoningReplayEntries[oldKey] = codexReasoningReplayEntry{
		Timestamp: time.Now().Add(-time.Minute),
		Bytes:     CodexReasoningReplayCacheMaxTotalBytes,
		Version:   1,
	}
	codexReasoningReplayTotalBytes = CodexReasoningReplayCacheMaxTotalBytes
	codexReasoningReplayVersion = 1
	codexReasoningReplayMu.Unlock()

	encryptedContent := validCodexReasoningReplayEncryptedContentForTest(10)
	item := []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + encryptedContent + `"}`)
	if !CacheCodexReasoningReplayItem("gpt-5.4", "new-session", item) {
		t.Fatal("cache insert failed")
	}

	codexReasoningReplayMu.Lock()
	_, oldExists := codexReasoningReplayEntries[oldKey]
	newEntry, newExists := codexReasoningReplayEntries[codexReasoningReplayCacheKey("gpt-5.4", "new-session")]
	totalBytes := codexReasoningReplayTotalBytes
	codexReasoningReplayMu.Unlock()
	if oldExists {
		t.Fatal("oldest entry was not evicted over total byte budget")
	}
	if !newExists {
		t.Fatal("newest entry was evicted instead of oldest entry")
	}
	if totalBytes != int64(newEntry.Bytes) || totalBytes > CodexReasoningReplayCacheMaxTotalBytes {
		t.Fatalf("total bytes = %d, new entry bytes = %d", totalBytes, newEntry.Bytes)
	}
}
