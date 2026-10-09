package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestModelCatalogConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	sources := ModelCatalogs{Catalog: filepath.Join(t.TempDir(), "models.json"), CodexCatalog: "https://example.com/codex.json", DevinCatalog: "http://localhost/devin.json"}
	data, errMarshal := yaml.Marshal(map[string]any{"models": sources})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if cfg.Models != sources {
		t.Fatalf("loaded sources: %+v", cfg.Models)
	}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	reloaded, errReload := LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	// A re-save must keep the models section intact after a reload.
	if errSave := SaveConfigPreserveComments(path, reloaded); errSave != nil {
		t.Fatal(errSave)
	}
	saved, errRead = os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var parsed struct {
		Models ModelCatalogs `yaml:"models"`
	}
	if errUnmarshal := yaml.Unmarshal(saved, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if parsed.Models != sources {
		t.Fatalf("saved sources: %+v", parsed.Models)
	}
	cfg.Models = ModelCatalogs{}
	if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
		t.Fatal(errSave)
	}
	cleared, errClear := LoadConfig(path)
	if errClear != nil {
		t.Fatal(errClear)
	}
	if cleared.Models != (ModelCatalogs{}) {
		t.Fatalf("cleared sources retained: %+v", cleared.Models)
	}
}

func TestModelCatalogConfigValidation(t *testing.T) {
	for _, field := range []string{"catalog", "codex-catalog", "devin-catalog"} {
		for _, source := range []string{"relative.json", "./models.json", "~/models.json", "ftp://example.com/models", "file:///tmp/models.json", "https:///models"} {
			data := []byte(fmt.Sprintf("models:\n  %s: %q\n", field, source))
			var cfg Config
			if errUnmarshal := yaml.Unmarshal(data, &cfg); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if errValidate := cfg.Models.Validate(); errValidate == nil {
				t.Fatalf("validation accepted %s: %s", field, source)
			}
		}
	}
	for _, raw := range []string{"models: {}", "models: {catalog: ''}", "models: {codex-catalog: 'https://example.com/models.json'}"} {
		var cfg Config
		if errUnmarshal := yaml.Unmarshal([]byte(raw), &cfg); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if errValidate := cfg.Models.Validate(); errValidate != nil {
			t.Fatal(errValidate)
		}
	}
}
