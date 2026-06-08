package config

import "testing"

func TestParseConfigBytesUsesCPATokenKey(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("cpa-token: config-token\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes returned error: %v", err)
	}
	if cfg.CPAToken != "config-token" {
		t.Fatalf("CPAToken = %q, want %q", cfg.CPAToken, "config-token")
	}
}

func TestParseConfigBytesIgnoresLegacyCNBTokenKey(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("cnb-token: legacy-token\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes returned error: %v", err)
	}
	if cfg.CPAToken != "" {
		t.Fatalf("CPAToken = %q, want empty string for legacy cnb-token", cfg.CPAToken)
	}
}
