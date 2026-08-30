package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	log "github.com/sirupsen/logrus"
)

// SignatureEntry holds a cached thinking signature with timestamp
type SignatureEntry struct {
	Signature string
	Timestamp time.Time
	bytes     int
}

const (
	// SignatureCacheTTL is how long signatures are valid
	SignatureCacheTTL = 3 * time.Hour

	// SignatureTextHashLen is the length of the hash key (16 hex chars = 64-bit key space)
	SignatureTextHashLen = 16

	// MinValidSignatureLen is the minimum length for a signature to be considered valid
	MinValidSignatureLen = 50

	// CacheCleanupInterval controls how often stale entries are purged
	CacheCleanupInterval = 10 * time.Minute

	// SignatureCacheRefreshInterval limits sliding-TTL writes on frequently read entries.
	SignatureCacheRefreshInterval = 10 * time.Minute

	// SignatureCacheMaxEntriesPerGroup bounds entries retained for one model group.
	SignatureCacheMaxEntriesPerGroup = 4096

	// SignatureCacheMaxBytesPerGroup bounds signature payload bytes retained for one model group.
	SignatureCacheMaxBytesPerGroup = 64 << 20

	// SignatureCacheEvictBatchSize leaves headroom after reaching the entry limit.
	SignatureCacheEvictBatchSize = 128
)

// signatureCache stores signatures by model group -> textHash -> SignatureEntry
var signatureCache sync.Map

// cacheCleanupOnce ensures the background cleanup goroutine starts only once
var cacheCleanupOnce sync.Once

type signatureKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVDel(ctx context.Context, keys ...string) (int64, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentSignatureKVClient = func() (signatureKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// groupCache is the inner map type
type groupCache struct {
	mu         sync.RWMutex
	entries    map[string]SignatureEntry
	totalBytes int64
}

// hashText creates a stable, Unicode-safe key from text content
func hashText(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])[:SignatureTextHashLen]
}

// getOrCreateGroupCache gets or creates a cache bucket for a model group
func getOrCreateGroupCache(groupKey string) *groupCache {
	// Start background cleanup on first access
	cacheCleanupOnce.Do(startCacheCleanup)

	if val, ok := signatureCache.Load(groupKey); ok {
		return val.(*groupCache)
	}
	sc := &groupCache{entries: make(map[string]SignatureEntry)}
	actual, _ := signatureCache.LoadOrStore(groupKey, sc)
	return actual.(*groupCache)
}

// startCacheCleanup launches a background goroutine that periodically
// removes caches where all entries have expired.
func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(CacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCaches()
		}
	}()
}

// purgeExpiredCaches removes caches with no valid (non-expired) entries.
func purgeExpiredCaches() {
	now := time.Now()
	signatureCache.Range(func(key, value any) bool {
		sc := value.(*groupCache)
		sc.mu.Lock()
		// Remove expired entries
		for k, entry := range sc.entries {
			if now.Sub(entry.Timestamp) > SignatureCacheTTL {
				deleteSignatureEntryLocked(sc, k, entry)
			}
		}
		isEmpty := len(sc.entries) == 0
		sc.mu.Unlock()
		// Remove cache bucket if empty
		if isEmpty {
			signatureCache.Delete(key)
		}
		return true
	})
	purgeExpiredCodexReasoningReplayCache(now)
	purgeExpiredXAIReasoningReplayCache(now)
	purgeExpiredAntigravityReasoningReplayCache(now)
	purgeExpiredOpenAICompatReasoningReplayCache(now)
}

// CacheSignature stores a thinking signature for a given model group and text.
// Used for Claude models that require signed thinking blocks in multi-turn conversations.
func CacheSignature(modelName, text, signature string) {
	CacheSignatureBestEffort(context.Background(), modelName, text, signature)
}

// CacheSignatureBestEffort stores a thinking signature for completed response paths.
func CacheSignatureBestEffort(ctx context.Context, modelName, text, signature string) bool {
	if text == "" || signature == "" {
		return false
	}
	if len(signature) < MinValidSignatureLen {
		return false
	}

	if client, homeMode, errClient := currentSignatureKVClient(); homeMode && errClient == nil {
		written, errSet := client.KVSet(ctx, signatureKVKey(modelName, text), []byte(signature), homekv.KVSetOptions{EX: SignatureCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort signature set failed prefix=cpa:signature:*: %v", errSet)
			// Fall through to process-local cache.
		} else {
			return written
		}
	}

	groupKey := GetModelGroup(modelName)
	textHash := hashText(text)
	entryBytes := len(textHash) + len(signature)
	if entryBytes > SignatureCacheMaxBytesPerGroup {
		return false
	}

	sc := getOrCreateGroupCache(groupKey)
	sc.mu.Lock()
	if existing, exists := sc.entries[textHash]; exists {
		sc.totalBytes -= int64(existing.bytes)
	}
	sc.entries[textHash] = SignatureEntry{
		Signature: signature,
		Timestamp: time.Now(),
		bytes:     entryBytes,
	}
	sc.totalBytes += int64(entryBytes)
	enforceSignatureCacheLimitsLocked(sc, SignatureCacheMaxEntriesPerGroup, SignatureCacheMaxBytesPerGroup, SignatureCacheEvictBatchSize)
	sc.mu.Unlock()
	return true
}

// GetCachedSignature retrieves a cached signature for a given model group and text.
// Returns empty string if not found or expired.
func GetCachedSignature(modelName, text string) string {
	signature, errSignature := GetCachedSignatureRequired(context.Background(), modelName, text)
	if errSignature != nil {
		return ""
	}
	return signature
}

// GetCachedSignatureRequired retrieves a cached signature for request-time paths.
func GetCachedSignatureRequired(ctx context.Context, modelName, text string) (string, error) {
	groupKey := GetModelGroup(modelName)

	if text == "" {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}

	if client, homeMode, errClient := currentSignatureKVClient(); homeMode && errClient == nil {
		key := signatureKVKey(modelName, text)
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			// Fall through to process-local cache when shared KV is unhealthy.
			log.Errorf("home kv signature get failed prefix=cpa:signature:*: %v", errGet)
		} else if found {
			if _, errExpire := client.KVExpire(ctx, key, SignatureCacheTTL); errExpire != nil {
				log.Errorf("home kv signature expire failed prefix=cpa:signature:*: %v", errExpire)
			}
			return string(raw), nil
		} else {
			if groupKey == "gemini" {
				return "skip_thought_signature_validator", nil
			}
			return "", nil
		}
	}

	val, ok := signatureCache.Load(groupKey)
	if !ok {
		if groupKey == "gemini" {
			return "skip_thought_signature_validator", nil
		}
		return "", nil
	}
	sc := val.(*groupCache)

	textHash := hashText(text)
	if signature, exists := getLocalCachedSignature(sc, textHash, time.Now()); exists {
		return signature, nil
	}
	if groupKey == "gemini" {
		return "skip_thought_signature_validator", nil
	}
	return "", nil
}

func getLocalCachedSignature(sc *groupCache, textHash string, now time.Time) (string, bool) {
	if sc == nil {
		return "", false
	}

	sc.mu.RLock()
	entry, exists := sc.entries[textHash]
	if !exists {
		sc.mu.RUnlock()
		return "", false
	}
	age := now.Sub(entry.Timestamp)
	if age <= SignatureCacheTTL && age < SignatureCacheRefreshInterval {
		sc.mu.RUnlock()
		return entry.Signature, true
	}
	sc.mu.RUnlock()

	sc.mu.Lock()
	defer sc.mu.Unlock()
	entry, exists = sc.entries[textHash]
	if !exists {
		return "", false
	}
	age = now.Sub(entry.Timestamp)
	if age > SignatureCacheTTL {
		deleteSignatureEntryLocked(sc, textHash, entry)
		return "", false
	}
	if age >= SignatureCacheRefreshInterval {
		entry.Timestamp = now
		sc.entries[textHash] = entry
	}
	return entry.Signature, true
}

func deleteSignatureEntryLocked(sc *groupCache, textHash string, entry SignatureEntry) {
	delete(sc.entries, textHash)
	sc.totalBytes -= int64(entry.bytes)
	if sc.totalBytes < 0 {
		sc.totalBytes = 0
	}
}

func enforceSignatureCacheLimitsLocked(sc *groupCache, maxEntries int, maxBytes int64, evictBatchSize int) {
	if sc == nil || (len(sc.entries) <= maxEntries && sc.totalBytes <= maxBytes) {
		return
	}
	type candidate struct {
		textHash  string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(sc.entries))
	for textHash, entry := range sc.entries {
		candidates = append(candidates, candidate{textHash: textHash, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})

	minimumEvictions := len(sc.entries) - maxEntries
	if minimumEvictions > 0 && minimumEvictions < evictBatchSize {
		minimumEvictions = evictBatchSize
	}
	for index, candidate := range candidates {
		if index >= minimumEvictions && len(sc.entries) <= maxEntries && sc.totalBytes <= maxBytes {
			break
		}
		if entry, exists := sc.entries[candidate.textHash]; exists {
			deleteSignatureEntryLocked(sc, candidate.textHash, entry)
		}
	}
}

// ClearSignatureCache clears signature cache for a specific model group or all groups.
func ClearSignatureCache(modelName string) {
	if modelName == "" {
		signatureCache.Range(func(key, _ any) bool {
			signatureCache.Delete(key)
			return true
		})
		return
	}
	groupKey := GetModelGroup(modelName)
	signatureCache.Delete(groupKey)
}

// DeleteCachedSignatureRequired removes one exact cached signature.
func DeleteCachedSignatureRequired(ctx context.Context, modelName, text string) error {
	if text == "" {
		return nil
	}
	if client, homeMode, errClient := currentSignatureKVClient(); homeMode {
		if errClient != nil {
			return errClient
		}
		_, errDel := client.KVDel(ctx, signatureKVKey(modelName, text))
		return errDel
	}
	groupKey := GetModelGroup(modelName)
	textHash := hashText(text)
	val, ok := signatureCache.Load(groupKey)
	if !ok {
		return nil
	}
	sc := val.(*groupCache)
	sc.mu.Lock()
	if entry, exists := sc.entries[textHash]; exists {
		deleteSignatureEntryLocked(sc, textHash, entry)
	}
	isEmpty := len(sc.entries) == 0
	sc.mu.Unlock()
	if isEmpty {
		signatureCache.Delete(groupKey)
	}
	return nil
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
func HasValidSignature(modelName, signature string) bool {
	return (signature != "" && len(signature) >= MinValidSignatureLen) || (signature == "skip_thought_signature_validator" && GetModelGroup(modelName) == "gemini")
}

func GetModelGroup(modelName string) string {
	if strings.Contains(modelName, "gpt") {
		return "gpt"
	} else if strings.Contains(modelName, "claude") {
		return "claude"
	} else if strings.Contains(modelName, "gemini") {
		return "gemini"
	}
	return modelName
}

func signatureKVKey(modelName, text string) string {
	return fmt.Sprintf("cpa:signature:%s:%s", GetModelGroup(modelName), homekv.HashKeyPart(text))
}

var signatureCacheEnabled atomic.Bool
var signatureBypassStrictMode atomic.Bool

func init() {
	signatureCacheEnabled.Store(true)
	signatureBypassStrictMode.Store(false)
}

// SetSignatureCacheEnabled switches Antigravity signature handling between cache mode and bypass mode.
func SetSignatureCacheEnabled(enabled bool) {
	previous := signatureCacheEnabled.Swap(enabled)
	if previous == enabled {
		return
	}
	if !enabled {
		log.Info("antigravity signature cache DISABLED - bypass mode active, cached signatures will not be used for request translation")
	}
}

// SignatureCacheEnabled returns whether signature cache validation is enabled.
func SignatureCacheEnabled() bool {
	return signatureCacheEnabled.Load()
}

// SetSignatureBypassStrictMode controls whether bypass mode uses strict protobuf-tree validation.
func SetSignatureBypassStrictMode(strict bool) {
	previous := signatureBypassStrictMode.Swap(strict)
	if previous == strict {
		return
	}
	if strict {
		log.Debug("antigravity bypass signature validation: strict mode (protobuf tree)")
	} else {
		log.Debug("antigravity bypass signature validation: basic mode (R/E + 0x12)")
	}
}

// SignatureBypassStrictMode returns whether bypass mode uses strict protobuf-tree validation.
func SignatureBypassStrictMode() bool {
	return signatureBypassStrictMode.Load()
}
