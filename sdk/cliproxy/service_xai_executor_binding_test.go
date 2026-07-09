package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestEnsureExecutorsForAuth_XAIBindsIndependentExecutor(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "xai-auth-1",
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("xai")
	if !ok || resolved == nil {
		t.Fatal("expected xai executor after bind")
	}
	if _, isXAIAuto := resolved.(*executor.XAIAutoExecutor); !isXAIAuto {
		t.Fatalf("executor type = %T, want *executor.XAIAutoExecutor", resolved)
	}
	if _, isCodex := resolved.(*executor.CodexAutoExecutor); isCodex {
		t.Fatal("xai must not bind the codex auto executor")
	}
}
