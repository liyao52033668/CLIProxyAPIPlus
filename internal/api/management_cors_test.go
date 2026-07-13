package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeCORSOrigin(t *testing.T) {
	cases := map[string]string{
		"http://localhost:5173":       "http://localhost:5173",
		"HTTP://LocalHost:5173/":      "http://localhost:5173",
		"localhost:5173":              "http://localhost:5173",
		"https://admin.example.com":   "https://admin.example.com",
		"https://admin.example.com/x": "https://admin.example.com",
		"":                            "",
		"ftp://example.com":           "",
	}
	for in, want := range cases {
		if got := normalizeCORSOrigin(in); got != want {
			t.Fatalf("normalizeCORSOrigin(%q)=%q want %q", in, got, want)
		}
	}
}

func TestOriginAllowedByList(t *testing.T) {
	allowed := []string{"http://localhost:5173", "https://admin.example.com"}
	if !originAllowedByList("http://localhost:5173", allowed) {
		t.Fatal("expected localhost allowed")
	}
	if !originAllowedByList("HTTP://LOCALHOST:5173", allowed) {
		t.Fatal("expected case-insensitive match")
	}
	if !originAllowedByList("https://admin.example.com/path", allowed) {
		t.Fatal("expected host match ignoring path")
	}
	if originAllowedByList("http://evil.example.com", allowed) {
		t.Fatal("unexpected allow for foreign origin")
	}
	if originAllowedByList("http://localhost:5173", nil) {
		t.Fatal("empty list must deny")
	}
}

func TestApplyManagementCORS_OptionsAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}}
	s.cfg.RemoteManagement.CorsAllowedOrigins = []string{"https://cpamc.xiaoying.org.cn", "http://localhost:5173"}

	engine := gin.New()
	engine.Use(s.corsMiddleware())
	engine.GET("/v0/management/config", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Allowed origin preflight must be 204 with reflected Origin, not 404.
	req := httptest.NewRequest(http.MethodOptions, "/v0/management/config", nil)
	req.Header.Set("Origin", "https://cpamc.xiaoying.org.cn")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("allowed OPTIONS status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://cpamc.xiaoying.org.cn" {
		t.Fatalf("Allow-Origin=%q", got)
	}

	// Disallowed origin preflight must not expose CORS headers.
	req = httptest.NewRequest(http.MethodOptions, "/v0/management/config", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("denied OPTIONS status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected Allow-Origin for denied origin: %q", got)
	}

	// Actual GET with allowlist should pass through and include CORS header.
	req = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("GET Allow-Origin=%q", got)
	}
}
