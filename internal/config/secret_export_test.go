package config

import "testing"

func TestParseConfigBytesDefaultsRemoteSecretExportOff(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("remote-management:\n  allow-remote: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.RemoteManagement.AllowSecretExport {
		t.Fatal("AllowSecretExport = true, want secure default false")
	}
}

func TestParseConfigBytesPreservesExplicitRemoteSecretExportOn(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte("remote-management:\n  allow-secret-export: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.RemoteManagement.AllowSecretExport {
		t.Fatal("AllowSecretExport = false, want explicit true")
	}
}
