package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCompleteFreebuffLoginValidatesInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing api key", `{"base_url":"https://www.codebuff.com"}`, http.StatusBadRequest},
		{"private base url", `{"api_key":"tok","base_url":"http://127.0.0.1:9000"}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/freebuff-auth/login/complete", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")

		h.CompleteFreebuffLogin(c)

		if rec.Code != tc.status {
			t.Fatalf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
	}
	if len(h.cfg.FreebuffKey) != 0 {
		t.Fatalf("rejected requests must not append entries, got %d", len(h.cfg.FreebuffKey))
	}
}
