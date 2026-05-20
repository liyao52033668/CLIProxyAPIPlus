package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
)

const apiAliasPrefix = "redacted_api_"

func UsageSnapshot(snapshot *dto.StatisticsSnapshot) *dto.StatisticsSnapshot {
	if snapshot == nil {
		return nil
	}

	redacted := &dto.StatisticsSnapshot{
		TotalRequests:  snapshot.TotalRequests,
		SuccessCount:   snapshot.SuccessCount,
		FailureCount:   snapshot.FailureCount,
		TotalTokens:    snapshot.TotalTokens,
		APIs:           make(map[string]dto.APISnapshot, len(snapshot.APIs)),
		RequestsByDay:  cloneIntMap(snapshot.RequestsByDay),
		RequestsByHour: cloneIntMap(snapshot.RequestsByHour),
		TokensByDay:    cloneIntMap(snapshot.TokensByDay),
		TokensByHour:   cloneIntMap(snapshot.TokensByHour),
	}

	for apiKey, apiSnapshot := range snapshot.APIs {
		alias := APIAlias(apiKey)
		cloned := cloneAPISnapshot(apiSnapshot)
		cloned.DisplayName = APIKeyDisplayName(apiKey)
		redacted.APIs[alias] = cloned
	}

	return redacted
}

func APIAlias(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	if trimmed == "unknown" || strings.HasPrefix(trimmed, apiAliasPrefix) {
		return trimmed
	}

	sum := sha256.Sum256([]byte(trimmed))
	return apiAliasPrefix + hex.EncodeToString(sum[:])[:12]
}

func APIKeyDisplayName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "unknown" {
		return "unknown"
	}

	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 4 {
		return strings.Repeat("*", runeCount)
	}
	if runeCount <= 8 {
		prefix := []rune(trimmed)[:1]
		suffix := []rune(trimmed)[runeCount-1:]
		return string(prefix) + strings.Repeat("*", runeCount-2) + string(suffix)
	}

	runes := []rune(trimmed)
	prefix := string(runes[:4])
	suffix := string(runes[runeCount-4:])
	return prefix + strings.Repeat("*", runeCount-8) + suffix
}

func cloneIntMap(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return map[string]int64{}
	}

	cloned := make(map[string]int64, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func cloneAPISnapshot(src dto.APISnapshot) dto.APISnapshot {
	cloned := dto.APISnapshot{
		DisplayName:   src.DisplayName,
		TotalRequests: src.TotalRequests,
		SuccessCount:  src.SuccessCount,
		FailureCount:  src.FailureCount,
		TotalTokens:   src.TotalTokens,
		Models:        make(map[string]dto.ModelSnapshot, len(src.Models)),
	}

	for modelName, modelSnapshot := range src.Models {
		cloned.Models[modelName] = cloneModelSnapshot(modelSnapshot)
	}

	return cloned
}

func cloneModelSnapshot(src dto.ModelSnapshot) dto.ModelSnapshot {
	cloned := dto.ModelSnapshot{
		TotalRequests: src.TotalRequests,
		SuccessCount:  src.SuccessCount,
		FailureCount:  src.FailureCount,
		TotalTokens:   src.TotalTokens,
		Details:       make([]dto.RequestDetail, len(src.Details)),
	}
	copy(cloned.Details, src.Details)
	return cloned
}
