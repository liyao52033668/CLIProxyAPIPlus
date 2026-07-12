package thinking_test

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
)

func TestKimiClaudeMessagesMaxRejectsUnsupportedExplicitLevel(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-max-clamp"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	_, err := thinking.ApplyThinking(body, "kimi-k2.5", "claude", "claude", "claude")
	if err == nil {
		t.Fatal("ApplyThinking() error = nil, want ErrLevelNotSupported")
	}
	var thinkingErr *thinking.ThinkingError
	if !errors.As(err, &thinkingErr) || thinkingErr.Code != thinking.ErrLevelNotSupported {
		t.Fatalf("ApplyThinking() error = %v, want ErrLevelNotSupported", err)
	}
}
