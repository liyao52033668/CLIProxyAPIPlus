package util

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveAutoModelNonAuto(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected string
		isAuto   bool
	}{
		{"gpt-4o", "gpt-4o", "gpt-4o", false},
		{"gemini-2.0-flash", "gemini-2.0-flash", "gemini-2.0-flash", false},
		{"claude-3-5-sonnet", "claude-3-5-sonnet", "claude-3-5-sonnet", false},
		{"empty string", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, isAuto := ResolveAutoModel(tt.model)
			if resolved != tt.expected {
				t.Errorf("ResolveAutoModel(%q) = %q, want %q", tt.model, resolved, tt.expected)
			}
			if isAuto != tt.isAuto {
				t.Errorf("ResolveAutoModel(%q) isAuto = %v, want %v", tt.model, isAuto, tt.isAuto)
			}
		})
	}
}

func TestResolveAutoModelWithRegistry(t *testing.T) {
	r := registry.GetGlobalRegistry()

	r.RegisterClient("test-client-auto", "openai", []*registry.ModelInfo{
		{ID: "test-model-1", DisplayName: "Test Model 1"},
		{ID: "test-model-2", DisplayName: "Test Model 2"},
	})

	resolved, isAuto := ResolveAutoModel("auto")
	if !isAuto {
		t.Error("expected isAuto=true for 'auto' model")
	}
	if resolved != "test-model-1" && resolved != "test-model-2" {
		t.Errorf("expected test-model-1 or test-model-2, got %q", resolved)
	}

	r.DisableAutoModel(resolved, "test-client-auto")

	nextResolved, _ := ResolveAutoModel("auto")
	if nextResolved == resolved {
		t.Errorf("expected different model after disabling %q, but got same", resolved)
	}

	r.UnregisterClient("test-client-auto")
}

func TestResolveAutoModelExcludesPrefixedAuto(t *testing.T) {
	r := registry.GetGlobalRegistry()
	clientID := "test-client-prefixed-auto"
	r.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{ID: "gh/auto", DisplayName: "Auto"},
		{ID: "gh/test-model", DisplayName: "Test Model"},
	})
	defer r.UnregisterClient(clientID)

	resolved, isAuto := ResolveAutoModel("auto")
	if !isAuto {
		t.Error("expected isAuto=true for 'auto' model")
	}
	if strings.EqualFold(resolved, "auto") || strings.HasSuffix(strings.ToLower(resolved), "/auto") {
		t.Errorf("expected a concrete model, got %q", resolved)
	}
}

func TestResolveAutoModelFallback(t *testing.T) {
	resolved, isAuto := ResolveAutoModel("auto")
	if resolved != "" {
		t.Errorf("expected empty fallback when no models available, got %q", resolved)
	}
	if !isAuto {
		t.Error("expected isAuto=true even for fallback")
	}
}
