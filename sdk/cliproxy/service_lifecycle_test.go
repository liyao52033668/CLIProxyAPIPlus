package cliproxy

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type lifecycleTokenProvider struct{}

func (lifecycleTokenProvider) Load(context.Context, *config.Config) (*TokenClientResult, error) {
	return &TokenClientResult{}, nil
}

type lifecycleAPIKeyProvider struct{}

func (lifecycleAPIKeyProvider) Load(context.Context, *config.Config) (*APIKeyClientResult, error) {
	return &APIKeyClientResult{}, nil
}

func TestServiceRunDoesNotCallAfterStartWhenListenFails(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer listener.Close()
	_, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse listener port: %v", errPort)
	}

	authDir := t.TempDir()
	var afterStartCalled atomic.Bool
	service := &Service{
		cfg: &config.Config{
			Host:    "127.0.0.1",
			Port:    port,
			AuthDir: authDir,
		},
		configPath:     filepath.Join(authDir, "config.yaml"),
		tokenProvider:  lifecycleTokenProvider{},
		apiKeyProvider: lifecycleAPIKeyProvider{},
		watcherFactory: func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{}, nil
		},
		hooks: Hooks{OnAfterStart: func(*Service) {
			afterStartCalled.Store(true)
		}},
		accessManager: sdkaccess.NewManager(),
		coreManager:   coreauth.NewManager(nil, nil, nil),
	}

	errRun := service.Run(context.Background())
	if errRun == nil || !strings.Contains(errRun.Error(), "failed to start HTTP server") {
		t.Fatalf("Run() error = %v, want listen failure", errRun)
	}
	if afterStartCalled.Load() {
		t.Fatal("OnAfterStart was called after listener startup failed")
	}
}
