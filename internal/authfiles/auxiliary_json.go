package authfiles

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
)

var defaultIgnoredAuthJSONPaths = []string{
	".management/codex-inspection-latest.json",
	"management/codex-inspection-latest.json",
}

var auxiliaryJSONIgnoreState = struct {
	mu    sync.RWMutex
	paths []string
}{
	paths: append([]string(nil), defaultIgnoredAuthJSONPaths...),
}

func SetIgnoredAuxiliaryJSONPaths(paths []string) {
	merged := append([]string{}, defaultIgnoredAuthJSONPaths...)
	merged = append(merged, paths...)
	merged = NormalizeAuxiliaryJSONPaths(merged)

	auxiliaryJSONIgnoreState.mu.Lock()
	auxiliaryJSONIgnoreState.paths = merged
	auxiliaryJSONIgnoreState.mu.Unlock()
}

func IgnoredAuxiliaryJSONPaths() []string {
	auxiliaryJSONIgnoreState.mu.RLock()
	defer auxiliaryJSONIgnoreState.mu.RUnlock()
	return append([]string(nil), auxiliaryJSONIgnoreState.paths...)
}

func ShouldIgnoreAuxiliaryJSON(path, baseDir string) bool {
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return false
	}
	rel = normalizeAuxiliaryJSONPath(rel)
	if rel == "" {
		return false
	}
	for _, candidate := range IgnoredAuxiliaryJSONPaths() {
		if rel == candidate {
			return true
		}
	}
	return false
}

func NormalizeAuxiliaryJSONPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized := normalizeAuxiliaryJSONPath(path)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func ParseAuxiliaryJSONPathsEnv(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if strings.HasPrefix(value, "[") {
		var raw []string
		if err := json.Unmarshal([]byte(value), &raw); err == nil {
			return NormalizeAuxiliaryJSONPaths(raw)
		}
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r':
			return true
		default:
			return false
		}
	})
	return NormalizeAuxiliaryJSONPaths(parts)
}

func normalizeAuxiliaryJSONPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == "/" {
		return ""
	}
	for strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	return strings.TrimPrefix(path, "/")
}
