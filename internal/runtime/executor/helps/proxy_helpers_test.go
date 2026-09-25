package helps

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientRequestProxyOverridesAuthAndGlobal(t *testing.T) {
	t.Parallel()

	ctx := coreexecutor.WithRequestProxyURL(context.Background(), "http://request-proxy.example:8081")
	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("transport = %#v, want request proxy", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example:8081" {
		t.Fatalf("proxy URL = %v, want request proxy", proxyURL)
	}

	refreshCtx := coreexecutor.WithoutRequestProxyURL(ctx)
	refreshClient := NewProxyAwareHTTPClient(
		refreshCtx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	refreshTransport, ok := refreshClient.Transport.(*http.Transport)
	if !ok || refreshTransport.Proxy == nil {
		t.Fatalf("refresh transport = %#v, want auth proxy", refreshClient.Transport)
	}
	refreshProxy, errRefresh := refreshTransport.Proxy(req)
	if errRefresh != nil {
		t.Fatalf("refresh proxy: %v", errRefresh)
	}
	if refreshProxy == nil || refreshProxy.String() != "http://auth-proxy.example:8080" {
		t.Fatalf("refresh proxy URL = %v, want auth proxy", refreshProxy)
	}
}

func resetProxyHTTPClientCacheForTest() {
	httpClientCacheMutex.Lock()
	httpClientCache = make(map[string]*http.Client)
	httpClientCacheMutex.Unlock()
}

func TestNewProxyAwareHTTPClientConcurrentCacheMissBuildsOneTransport(t *testing.T) {
	resetProxyHTTPClientCacheForTest()
	originalBuilder := buildProxyTransportFunc
	t.Cleanup(func() {
		buildProxyTransportFunc = originalBuilder
		resetProxyHTTPClientCacheForTest()
	})

	var calls atomic.Int32
	buildProxyTransportFunc = func(string) *http.Transport {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &http.Transport{}
	}

	const workers = 32
	proxyURL := "http://concurrent-proxy.example.com:8080"
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}
	transports := make([]http.RoundTripper, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			transports[index] = NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0).Transport
		}(i)
	}
	wait.Wait()

	if calls.Load() != 1 {
		t.Fatalf("transport builder called %d times, want 1", calls.Load())
	}
	for i := 1; i < len(transports); i++ {
		if transports[i] != transports[0] {
			t.Fatalf("transport %d was not shared", i)
		}
	}
}

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
