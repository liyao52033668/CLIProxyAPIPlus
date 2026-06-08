package authfiles

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeAuxiliaryJSONPaths(t *testing.T) {
	paths := NormalizeAuxiliaryJSONPaths([]string{
		" management/codex-inspection-latest.json ",
		"./management/codex-inspection-latest.json",
		".management/codex-inspection-latest.json",
		"",
	})

	expected := []string{
		"management/codex-inspection-latest.json",
		".management/codex-inspection-latest.json",
	}
	if !reflect.DeepEqual(paths, expected) {
		t.Fatalf("NormalizeAuxiliaryJSONPaths() = %v, want %v", paths, expected)
	}
}

func TestParseAuxiliaryJSONPathsEnv(t *testing.T) {
	t.Run("json array", func(t *testing.T) {
		paths := ParseAuxiliaryJSONPathsEnv(`["management/codex-inspection-latest.json","custom/other.json"]`)
		expected := []string{"management/codex-inspection-latest.json", "custom/other.json"}
		if !reflect.DeepEqual(paths, expected) {
			t.Fatalf("ParseAuxiliaryJSONPathsEnv() = %v, want %v", paths, expected)
		}
	})

	t.Run("comma separated", func(t *testing.T) {
		paths := ParseAuxiliaryJSONPathsEnv("management/codex-inspection-latest.json, custom/other.json")
		expected := []string{"management/codex-inspection-latest.json", "custom/other.json"}
		if !reflect.DeepEqual(paths, expected) {
			t.Fatalf("ParseAuxiliaryJSONPathsEnv() = %v, want %v", paths, expected)
		}
	})
}

func TestShouldIgnoreAuxiliaryJSON(t *testing.T) {
	SetIgnoredAuxiliaryJSONPaths([]string{"custom/other.json"})
	t.Cleanup(func() {
		SetIgnoredAuxiliaryJSONPaths(nil)
	})

	baseDir := t.TempDir()
	legacyPath := filepath.Join(baseDir, ".management", "codex-inspection-latest.json")
	if !ShouldIgnoreAuxiliaryJSON(legacyPath, baseDir) {
		t.Fatal("ShouldIgnoreAuxiliaryJSON() = false, want true for default legacy path")
	}

	customPath := filepath.Join(baseDir, "custom", "other.json")
	if !ShouldIgnoreAuxiliaryJSON(customPath, baseDir) {
		t.Fatal("ShouldIgnoreAuxiliaryJSON() = false, want true for configured custom path")
	}
}
