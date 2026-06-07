package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientCachedProxyDoesNotLeakTimeout(t *testing.T) {
	httpClientCacheMutex.Lock()
	httpClientCache = make(map[string]*http.Client)
	httpClientCacheMutex.Unlock()

	proxyURL := "http://proxy.example.com:8080"
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}

	sessionClient := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 15*time.Second)
	if sessionClient.Timeout != 15*time.Second {
		t.Fatalf("session client timeout = %v, want %v", sessionClient.Timeout, 15*time.Second)
	}

	streamClient := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	if streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want 0", streamClient.Timeout)
	}
	if sessionClient.Transport != streamClient.Transport {
		t.Fatal("expected cached proxy transport to be reused")
	}
}
