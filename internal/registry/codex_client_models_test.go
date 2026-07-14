package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddedCodexClientModelsCatalogIsValid(t *testing.T) {
	data, revision := GetCodexClientModelsSnapshot()
	if revision == 0 {
		t.Fatal("embedded Codex client model catalog revision = 0, want non-zero")
	}
	if errValidate := ValidateCodexClientModelsJSON(data); errValidate != nil {
		t.Fatalf("embedded Codex client model catalog is invalid: %v", errValidate)
	}

	data[0] ^= 0xff
	second, secondRevision := GetCodexClientModelsSnapshot()
	if secondRevision != revision {
		t.Fatalf("snapshot revision = %d, want %d", secondRevision, revision)
	}
	if errValidate := ValidateCodexClientModelsJSON(second); errValidate != nil {
		t.Fatalf("mutating returned snapshot changed stored catalog: %v", errValidate)
	}
}

func TestValidateCodexClientModelsJSON(t *testing.T) {
	validDefault := testCodexClientModel("gpt-5.5", 1)
	validOther := testCodexClientModel("gpt-5.6-sol", 2)
	emptySlug := testCodexClientModel("gpt-5.5", 1)
	emptySlug["slug"] = ""
	missingField := testCodexClientModel("gpt-5.5", 1)
	delete(missingField, "base_instructions")
	wrongFieldType := testCodexClientModel("gpt-5.5", 1)
	wrongFieldType["context_window"] = "372000"
	unsupportedDefault := testCodexClientModel("gpt-5.5", 1)
	unsupportedDefault["default_reasoning_level"] = "high"

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte(`{"models":`)},
		{name: "empty", raw: []byte(`{"models":[]}`)},
		{name: "empty slug", raw: testCodexClientCatalog(t, emptySlug)},
		{name: "duplicate slug", raw: testCodexClientCatalog(t, validDefault, validDefault)},
		{name: "missing default", raw: testCodexClientCatalog(t, validOther)},
		{name: "missing required field", raw: testCodexClientCatalog(t, missingField)},
		{name: "wrong required field type", raw: testCodexClientCatalog(t, wrongFieldType)},
		{name: "default reasoning level not supported", raw: testCodexClientCatalog(t, unsupportedDefault)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errValidate := ValidateCodexClientModelsJSON(tt.raw); errValidate == nil {
				t.Fatal("ValidateCodexClientModelsJSON() error = nil, want error")
			}
		})
	}

	valid := testCodexClientCatalog(t, validDefault, validOther)
	if errValidate := ValidateCodexClientModelsJSON(valid); errValidate != nil {
		t.Fatalf("valid catalog rejected: %v", errValidate)
	}
}

func TestLoadCodexClientModelsRejectsInvalidWithoutReplacing(t *testing.T) {
	original, _ := GetCodexClientModelsSnapshot()
	t.Cleanup(func() {
		if _, errRestore := loadCodexClientModelsFromBytes(original, "test cleanup"); errRestore != nil {
			t.Fatalf("restore original catalog: %v", errRestore)
		}
	})

	valid := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	changed, errLoad := loadCodexClientModelsFromBytes(valid, "test")
	if errLoad != nil {
		t.Fatalf("load valid catalog: %v", errLoad)
	}
	if !changed {
		t.Fatal("load valid catalog changed = false, want true")
	}
	beforeInvalid, revision := GetCodexClientModelsSnapshot()

	if _, errInvalid := loadCodexClientModelsFromBytes([]byte(`{"models":[]}`), "test invalid"); errInvalid == nil {
		t.Fatal("load invalid catalog error = nil, want error")
	}
	afterInvalid, afterRevision := GetCodexClientModelsSnapshot()
	if string(afterInvalid) != string(beforeInvalid) {
		t.Fatal("invalid catalog replaced current snapshot")
	}
	if afterRevision != revision {
		t.Fatalf("revision after invalid catalog = %d, want %d", afterRevision, revision)
	}
}

func TestFetchCodexClientModelsFallsBackToNextURL(t *testing.T) {
	invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	}))
	defer invalidServer.Close()

	validCatalog := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	validServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(validCatalog)
	}))
	defer validServer.Close()

	previousURLs := codexClientModelsURLs
	codexClientModelsURLs = []string{invalidServer.URL, validServer.URL}
	t.Cleanup(func() { codexClientModelsURLs = previousURLs })

	data, sourceURL := fetchCodexClientModelsFromRemote(context.Background())
	if sourceURL != validServer.URL {
		t.Fatalf("source URL = %q, want %q", sourceURL, validServer.URL)
	}
	if string(data) != string(validCatalog) {
		t.Fatalf("catalog = %s, want %s", data, validCatalog)
	}
}

func testCodexClientModel(slug string, priority int) map[string]any {
	return map[string]any{
		"slug":                       slug,
		"display_name":               "Test " + slug,
		"description":                "Test model",
		"base_instructions":          "Test instructions",
		"minimal_client_version":     "0.144.0",
		"visibility":                 "list",
		"context_window":             372000,
		"max_context_window":         372000,
		"priority":                   priority,
		"default_reasoning_level":    "medium",
		"supported_reasoning_levels": []map[string]any{{"effort": "medium", "description": "Balanced"}},
	}
}

func testCodexClientCatalog(t *testing.T, models ...map[string]any) []byte {
	t.Helper()
	data, errMarshal := json.Marshal(map[string]any{"models": models})
	if errMarshal != nil {
		t.Fatalf("marshal test Codex client catalog: %v", errMarshal)
	}
	return data
}
