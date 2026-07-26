package config

import (
	"path/filepath"
	"testing"
)

func TestParseConfigBytesDefaultsWebsocketAuthOn(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("api-keys: []\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.WebsocketAuth {
		t.Fatal("WebsocketAuth = false, want secure default true")
	}
}

func TestParseConfigBytesPreservesExplicitWebsocketAuthOff(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("ws-auth: false\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.WebsocketAuth {
		t.Fatal("WebsocketAuth = true, want explicit false")
	}
}

func TestLoadConfigOptionalMissingFileDefaultsWebsocketAuthOn(t *testing.T) {
	t.Parallel()

	cfg, errLoad := LoadConfigOptional(filepath.Join(t.TempDir(), "missing.yaml"), true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if !cfg.WebsocketAuth {
		t.Fatal("WebsocketAuth = false, want secure default true")
	}
}
