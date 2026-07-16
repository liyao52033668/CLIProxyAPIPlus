package kimi

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDeviceFlowClientCommonHeadersUseBuildVersion(t *testing.T) {
	client := &DeviceFlowClient{deviceID: "device-1"}
	headers := client.commonHeaders()

	if got := headers["X-Msh-Platform"]; got != "CLIProxyAPI" {
		t.Fatalf("X-Msh-Platform = %q, want CLIProxyAPI", got)
	}
	if got := headers["X-Msh-Version"]; got != buildinfo.Version {
		t.Fatalf("X-Msh-Version = %q, want %q", got, buildinfo.Version)
	}
}

func TestNewDeviceFlowClientWithDeviceIDAndProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://proxy.example.com:8080"}}
	client := NewDeviceFlowClientWithDeviceIDAndProxyURL(cfg, "device-1", "direct")

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewDeviceFlowClientWithDeviceIDAndProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	client := NewDeviceFlowClientWithDeviceIDAndProxyURL(cfg, "device-1", "http://override.example.com:8081")

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", client.httpClient.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://override.example.com:8081" {
		t.Fatalf("proxy URL = %v, want http://override.example.com:8081", proxyURL)
	}
}
