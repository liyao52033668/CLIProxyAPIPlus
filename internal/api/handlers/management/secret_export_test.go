package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestSecretExportMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		remoteAddr        string
		forwardedFor      string
		allowSecretExport bool
		wantStatus        int
	}{
		{
			name:       "localhost allowed by default",
			remoteAddr: "127.0.0.1:4321",
			wantStatus: http.StatusOK,
		},
		{
			name:       "remote denied by default",
			remoteAddr: "198.51.100.10:4321",
			wantStatus: http.StatusForbidden,
		},
		{
			name:              "remote allowed when explicitly enabled",
			remoteAddr:        "198.51.100.10:4321",
			allowSecretExport: true,
			wantStatus:        http.StatusOK,
		},
		{
			name:         "forwarded loopback does not bypass remote restriction",
			remoteAddr:   "198.51.100.10:4321",
			forwardedFor: "127.0.0.1",
			wantStatus:   http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{RemoteManagement: config.RemoteManagement{
				AllowSecretExport: tc.allowSecretExport,
			}}}
			router := gin.New()
			router.GET("/secret", h.SecretExportMiddleware(), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"secret": "value"})
			})

			req := httptest.NewRequest(http.MethodGet, "/secret", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tc.forwardedFor)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}
