package util

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveAuthDirNormalizesPlatformSeparators(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	rootedWindowsPath := filepath.Clean(filepath.FromSlash("/CLIProxyAPI/data/auths"))
	if runtime.GOOS != "windows" {
		rootedWindowsPath = filepath.Join(home, "CLIProxyAPI", "data", "auths")
	}

	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "windows separators",
			input: `\CLIProxyAPI\data\auths`,
			want:  rootedWindowsPath,
		},
		{
			name:  "relative windows separators",
			input: `data\auths`,
			want:  filepath.Join("data", "auths"),
		},
		{
			name:  "unix separators",
			input: "/var/lib/cli-proxy/auths",
			want:  filepath.Clean(filepath.FromSlash("/var/lib/cli-proxy/auths")),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ResolveAuthDir(testCase.input)
			if err != nil {
				t.Fatalf("ResolveAuthDir returned error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("ResolveAuthDir(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestResolveAuthDirExpandsTildeWithPlatformSeparators(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := ResolveAuthDir(`~\nested\auths`)
	if err != nil {
		t.Fatalf("ResolveAuthDir returned error: %v", err)
	}
	want := filepath.Join(home, "nested", "auths")
	if got != want {
		t.Fatalf("ResolveAuthDir returned %q, want %q", got, want)
	}
}

func TestWritablePathNormalizesPlatformSeparators(t *testing.T) {
	t.Setenv("WRITABLE_PATH", `runtime\data`)

	if got, want := WritablePath(), filepath.Join("runtime", "data"); got != want {
		t.Fatalf("WritablePath() = %q, want %q", got, want)
	}
}
