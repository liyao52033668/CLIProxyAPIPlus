package cache

import (
	"bytes"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

const testModelName = "claude-sonnet-4-5"

func TestCacheSignature_BasicStorageAndRetrieval(t *testing.T) {
	ClearSignatureCache("")

	text := "This is some thinking text content"
	signature := "abc123validSignature1234567890123456789012345678901234567890"

	// Store signature
	CacheSignature(testModelName, text, signature)

	// Retrieve signature
	retrieved := GetCachedSignature(testModelName, text)
	if retrieved != signature {
		t.Errorf("Expected signature '%s', got '%s'", signature, retrieved)
	}
}

func TestCacheSignature_DifferentModelGroups(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text across models"
	sig1 := "signature1_1234567890123456789012345678901234567890123456"
	sig2 := "signature2_1234567890123456789012345678901234567890123456"

	geminiModel := "gemini-3-pro-preview"
	CacheSignature(testModelName, text, sig1)
	CacheSignature(geminiModel, text, sig2)

	if GetCachedSignature(testModelName, text) != sig1 {
		t.Error("Claude signature mismatch")
	}
	if GetCachedSignature(geminiModel, text) != sig2 {
		t.Error("Gemini signature mismatch")
	}
}

func TestCacheSignature_NotFound(t *testing.T) {
	ClearSignatureCache("")

	// Non-existent session
	if got := GetCachedSignature(testModelName, "some text"); got != "" {
		t.Errorf("Expected empty string for nonexistent session, got '%s'", got)
	}

	// Existing session but different text
	CacheSignature(testModelName, "text-a", "sigA12345678901234567890123456789012345678901234567890")
	if got := GetCachedSignature(testModelName, "text-b"); got != "" {
		t.Errorf("Expected empty string for different text, got '%s'", got)
	}
}

func TestCacheSignature_EmptyInputs(t *testing.T) {
	ClearSignatureCache("")

	// All empty/invalid inputs should be no-ops
	CacheSignature(testModelName, "", "sig12345678901234567890123456789012345678901234567890")
	CacheSignature(testModelName, "text", "")
	CacheSignature(testModelName, "text", "short") // Too short

	if got := GetCachedSignature(testModelName, "text"); got != "" {
		t.Errorf("Expected empty after invalid cache attempts, got '%s'", got)
	}
}

func TestCacheSignature_ShortSignatureRejected(t *testing.T) {
	ClearSignatureCache("")

	text := "Some text"
	shortSig := "abc123" // Less than 50 chars

	CacheSignature(testModelName, text, shortSig)

	if got := GetCachedSignature(testModelName, text); got != "" {
		t.Errorf("Short signature should be rejected, got '%s'", got)
	}
}

func TestClearSignatureCache_ModelGroup(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature(testModelName, "text", sig)
	CacheSignature(testModelName, "text-2", sig)

	ClearSignatureCache("session-1")

	if got := GetCachedSignature(testModelName, "text"); got != sig {
		t.Error("signature should remain when clearing unknown session")
	}
}

func TestClearSignatureCache_AllSessions(t *testing.T) {
	ClearSignatureCache("")

	sig := "validSig1234567890123456789012345678901234567890123456"
	CacheSignature(testModelName, "text", sig)
	CacheSignature(testModelName, "text-2", sig)

	ClearSignatureCache("")

	if got := GetCachedSignature(testModelName, "text"); got != "" {
		t.Error("text should be cleared")
	}
	if got := GetCachedSignature(testModelName, "text-2"); got != "" {
		t.Error("text-2 should be cleared")
	}
}

func TestHasValidSignature(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		signature string
		expected  bool
	}{
		{"valid long signature", testModelName, "abc123validSignature1234567890123456789012345678901234567890", true},
		{"exactly 50 chars", testModelName, "12345678901234567890123456789012345678901234567890", true},
		{"49 chars - invalid", testModelName, "1234567890123456789012345678901234567890123456789", false},
		{"empty string", testModelName, "", false},
		{"short signature", testModelName, "abc", false},
		{"gemini sentinel", "gemini-3-pro-preview", "skip_thought_signature_validator", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasValidSignature(tt.modelName, tt.signature)
			if result != tt.expected {
				t.Errorf("HasValidSignature(%q) = %v, expected %v", tt.signature, result, tt.expected)
			}
		})
	}
}

func TestCacheSignature_TextHashCollisionResistance(t *testing.T) {
	ClearSignatureCache("")

	// Different texts should produce different hashes
	text1 := "First thinking text"
	text2 := "Second thinking text"
	sig1 := "signature1_1234567890123456789012345678901234567890123456"
	sig2 := "signature2_1234567890123456789012345678901234567890123456"

	CacheSignature(testModelName, text1, sig1)
	CacheSignature(testModelName, text2, sig2)

	if GetCachedSignature(testModelName, text1) != sig1 {
		t.Error("text1 signature mismatch")
	}
	if GetCachedSignature(testModelName, text2) != sig2 {
		t.Error("text2 signature mismatch")
	}
}

func TestCacheSignature_UnicodeText(t *testing.T) {
	ClearSignatureCache("")

	text := "한글 텍스트와 이모지 🎉 그리고 特殊文字"
	sig := "unicodeSig123456789012345678901234567890123456789012345"

	CacheSignature(testModelName, text, sig)

	if got := GetCachedSignature(testModelName, text); got != sig {
		t.Errorf("Unicode text signature retrieval failed, got '%s'", got)
	}
}

func TestCacheSignature_Overwrite(t *testing.T) {
	ClearSignatureCache("")

	text := "Same text"
	sig1 := "firstSignature12345678901234567890123456789012345678901"
	sig2 := "secondSignature1234567890123456789012345678901234567890"

	CacheSignature(testModelName, text, sig1)
	CacheSignature(testModelName, text, sig2) // Overwrite

	if got := GetCachedSignature(testModelName, text); got != sig2 {
		t.Errorf("Expected overwritten signature '%s', got '%s'", sig2, got)
	}
}

func TestCacheSignature_ExpirationLogic(t *testing.T) {
	ClearSignatureCache("")

	text := "text"
	sig := "validSig1234567890123456789012345678901234567890123456"

	CacheSignature(testModelName, text, sig)

	if got := GetCachedSignature(testModelName, text); got != sig {
		t.Errorf("Fresh entry should be retrievable, got '%s'", got)
	}
}

func TestGetLocalCachedSignatureRefreshesTTLPeriodically(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	textHash := "hash"
	signature := "signature"
	sc := &groupCache{
		entries: map[string]SignatureEntry{
			textHash: {
				Signature: signature,
				Timestamp: now.Add(-SignatureCacheRefreshInterval / 2),
				bytes:     len(textHash) + len(signature),
			},
		},
		totalBytes: int64(len(textHash) + len(signature)),
	}

	if got, ok := getLocalCachedSignature(sc, textHash, now); !ok || got != signature {
		t.Fatalf("fresh cache read = %q, %v; want signature", got, ok)
	}
	freshTimestamp := sc.entries[textHash].Timestamp
	if !freshTimestamp.Equal(now.Add(-SignatureCacheRefreshInterval / 2)) {
		t.Fatalf("fresh cache read updated timestamp to %v", freshTimestamp)
	}

	refreshAt := now.Add(SignatureCacheRefreshInterval)
	if got, ok := getLocalCachedSignature(sc, textHash, refreshAt); !ok || got != signature {
		t.Fatalf("refreshing cache read = %q, %v; want signature", got, ok)
	}
	if refreshedTimestamp := sc.entries[textHash].Timestamp; !refreshedTimestamp.Equal(refreshAt) {
		t.Fatalf("refreshed timestamp = %v, want %v", refreshedTimestamp, refreshAt)
	}
}

func TestGetLocalCachedSignatureDeletesExpiredEntry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	textHash := "hash"
	entry := SignatureEntry{
		Signature: "signature",
		Timestamp: now.Add(-SignatureCacheTTL - time.Second),
		bytes:     13,
	}
	sc := &groupCache{
		entries:    map[string]SignatureEntry{textHash: entry},
		totalBytes: int64(entry.bytes),
	}

	if got, ok := getLocalCachedSignature(sc, textHash, now); ok || got != "" {
		t.Fatalf("expired cache read = %q, %v; want miss", got, ok)
	}
	if len(sc.entries) != 0 || sc.totalBytes != 0 {
		t.Fatalf("expired cache state = %d entries, %d bytes; want empty", len(sc.entries), sc.totalBytes)
	}
}

func TestEnforceSignatureCacheLimitsEvictsOldestBatch(t *testing.T) {
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	for index, textHash := range []string{"oldest", "older", "middle", "newer", "newest"} {
		entry := SignatureEntry{
			Signature: "signature",
			Timestamp: time.Unix(int64(index+1), 0),
			bytes:     10,
		}
		sc.entries[textHash] = entry
		sc.totalBytes += int64(entry.bytes)
	}

	enforceSignatureCacheLimitsLocked(sc, 4, 100, 2)

	if len(sc.entries) != 3 {
		t.Fatalf("cache entries = %d, want 3 after batch eviction", len(sc.entries))
	}
	if _, exists := sc.entries["oldest"]; exists {
		t.Fatal("oldest entry was not evicted")
	}
	if _, exists := sc.entries["older"]; exists {
		t.Fatal("second-oldest entry was not evicted")
	}
	if _, exists := sc.entries["newest"]; !exists {
		t.Fatal("newest entry was evicted")
	}
	if sc.totalBytes != 30 {
		t.Fatalf("cache bytes = %d, want 30", sc.totalBytes)
	}
}

func TestEnforceSignatureCacheLimitsEvictsToByteLimit(t *testing.T) {
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	for index, textHash := range []string{"oldest", "middle", "newest"} {
		entry := SignatureEntry{
			Signature: "signature",
			Timestamp: time.Unix(int64(index+1), 0),
			bytes:     40,
		}
		sc.entries[textHash] = entry
		sc.totalBytes += int64(entry.bytes)
	}

	enforceSignatureCacheLimitsLocked(sc, 10, 80, 2)

	if len(sc.entries) != 2 || sc.totalBytes != 80 {
		t.Fatalf("byte-limited cache = %d entries, %d bytes; want 2 entries, 80 bytes", len(sc.entries), sc.totalBytes)
	}
	if _, exists := sc.entries["oldest"]; exists {
		t.Fatal("oldest entry was not evicted for byte limit")
	}
}

func TestSignatureModeSetters_LogAtInfoLevel(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousCache := SignatureCacheEnabled()
	previousStrict := SignatureBypassStrictMode()
	SetSignatureCacheEnabled(true)
	SetSignatureBypassStrictMode(false)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureCacheEnabled(previousCache)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)
	SetSignatureBypassStrictMode(false)

	output := buffer.String()
	if !strings.Contains(output, "antigravity signature cache DISABLED") {
		t.Fatalf("expected info output for disabling signature cache, got: %q", output)
	}
	if strings.Contains(output, "strict mode (protobuf tree)") {
		t.Fatalf("expected strict bypass mode log to stay below info level, got: %q", output)
	}
	if strings.Contains(output, "basic mode (R/E + 0x12)") {
		t.Fatalf("expected basic bypass mode log to stay below info level, got: %q", output)
	}
}

func TestSignatureModeSetters_DoNotRepeatSameStateLogs(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousCache := SignatureCacheEnabled()
	previousStrict := SignatureBypassStrictMode()
	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureCacheEnabled(previousCache)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureCacheEnabled(false)
	SetSignatureBypassStrictMode(true)

	if buffer.Len() != 0 {
		t.Fatalf("expected repeated setter calls with unchanged state to stay silent, got: %q", buffer.String())
	}
}

func TestSignatureBypassStrictMode_LogsAtDebugLevel(t *testing.T) {
	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := logger.Level
	previousStrict := SignatureBypassStrictMode()
	SetSignatureBypassStrictMode(false)
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
		SetSignatureBypassStrictMode(previousStrict)
	})

	SetSignatureBypassStrictMode(true)
	SetSignatureBypassStrictMode(false)

	output := buffer.String()
	if !strings.Contains(output, "strict mode (protobuf tree)") {
		t.Fatalf("expected debug output for strict bypass mode, got: %q", output)
	}
	if !strings.Contains(output, "basic mode (R/E + 0x12)") {
		t.Fatalf("expected debug output for basic bypass mode, got: %q", output)
	}
}
