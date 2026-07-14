package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gorm.io/gorm"
)

func TestInitializeUsageDatabaseNormalizesConfiguredPathAndCreatesDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	configuredPath := `\CLIProxyAPI\data\auths`
	wantDir := filepath.Clean(filepath.FromSlash("/CLIProxyAPI/data/auths"))
	if runtime.GOOS != "windows" {
		wantDir = filepath.Join(home, "CLIProxyAPI", "data", "auths")
	} else {
		configuredPath = filepath.Join(home, "CLIProxyAPI", "data", "auths")
		wantDir = configuredPath
	}

	previousOpen := openRuntimeSQLiteUsageDatabase
	t.Cleanup(func() { openRuntimeSQLiteUsageDatabase = previousOpen })

	var openedPath string
	openRuntimeSQLiteUsageDatabase = func(path string) (*gorm.DB, error) {
		openedPath = path
		return nil, nil
	}

	_, err := initializeUsageDatabase(configuredPath, false, "", false, nil, false, nil)
	if err != nil {
		t.Fatalf("initializeUsageDatabase returned error: %v", err)
	}
	want := filepath.Join(wantDir, "usage.db")
	if openedPath != want {
		t.Fatalf("usage database path = %q, want %q", openedPath, want)
	}
	if info, err := os.Stat(wantDir); err != nil {
		t.Fatalf("stat usage database directory: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("usage database directory %q is not a directory", wantDir)
	}
}

func TestInitializeUsageDatabaseUsesPlatformDefaultDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	previousOpen := openRuntimeSQLiteUsageDatabase
	t.Cleanup(func() { openRuntimeSQLiteUsageDatabase = previousOpen })

	var openedPath string
	openRuntimeSQLiteUsageDatabase = func(path string) (*gorm.DB, error) {
		openedPath = path
		return nil, nil
	}

	_, err := initializeUsageDatabase("", false, "", false, nil, false, nil)
	if err != nil {
		t.Fatalf("initializeUsageDatabase returned error: %v", err)
	}
	want := filepath.Join(home, ".cli-proxy-api", "usage.db")
	if openedPath != want {
		t.Fatalf("usage database path = %q, want %q", openedPath, want)
	}
}
