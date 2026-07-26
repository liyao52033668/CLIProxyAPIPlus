package config

import "testing"

func TestParseConfigBytesDefaultsQueryAPIKeyOff(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("api-keys: []\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.AllowQueryAPIKey {
		t.Fatal("AllowQueryAPIKey = true, want secure default false")
	}
}

func TestParseConfigBytesPreservesExplicitQueryAPIKeyOn(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("allow-query-api-key: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.AllowQueryAPIKey {
		t.Fatal("AllowQueryAPIKey = false, want explicit true")
	}
}
