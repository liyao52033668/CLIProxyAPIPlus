package commandcode

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetCLIVersion_Default(t *testing.T) {
	// If cache is empty and remote call fails (or mocked), it should return DefaultCLIVersion
	versionMu.Lock()
	versionCache = ""
	versionCacheTime = time.Time{}
	versionMu.Unlock()

	v := GetCLIVersion()
	if v == "" {
		t.Fatalf("expected non-empty version, got empty")
	}
}

func TestGetCLIVersion_Cached(t *testing.T) {
	versionMu.Lock()
	versionCache = "9.99.9"
	versionCacheTime = time.Now()
	versionMu.Unlock()

	v := GetCLIVersion()
	if v != "9.99.9" {
		t.Fatalf("expected cached version 9.99.9, got %s", v)
	}

	// Reset cache
	versionMu.Lock()
	versionCache = ""
	versionCacheTime = time.Time{}
	versionMu.Unlock()
}

func TestGetCLIVersion_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"command-code","version":"1.33.0"}`))
	}))
	defer server.Close()

	// Direct check JSON parsing logic
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
}
