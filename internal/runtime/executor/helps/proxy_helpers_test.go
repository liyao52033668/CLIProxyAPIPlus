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
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

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
