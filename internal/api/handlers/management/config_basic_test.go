package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestLatestReleaseTokenAllowsNilConfigAndFallsBackToBuildInfo(t *testing.T) {
	t.Setenv("CPA_TOKEN", "")
	oldBuildToken := buildinfo.CPAToken
	buildinfo.CPAToken = "build-token"
	t.Cleanup(func() { buildinfo.CPAToken = oldBuildToken })

	got := latestReleaseToken(nil)
	if got != "build-token" {
		t.Fatalf("latestReleaseToken(nil) = %q, want %q", got, "build-token")
	}
}

func TestLatestReleaseTokenPrefersEnvThenConfigThenBuildInfo(t *testing.T) {
	oldBuildToken := buildinfo.CPAToken
	buildinfo.CPAToken = "build-token"
	t.Cleanup(func() { buildinfo.CPAToken = oldBuildToken })

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{CPAToken: "config-token"},
	}

	t.Setenv("CPA_TOKEN", "env-token")
	if got := latestReleaseToken(cfg); got != "env-token" {
		t.Fatalf("latestReleaseToken with env = %q, want %q", got, "env-token")
	}

	t.Setenv("CPA_TOKEN", "")
	if got := latestReleaseToken(cfg); got != "config-token" {
		t.Fatalf("latestReleaseToken with config = %q, want %q", got, "config-token")
	}

	cfg.CPAToken = ""
	if got := latestReleaseToken(cfg); got != "build-token" {
		t.Fatalf("latestReleaseToken with buildinfo = %q, want %q", got, "build-token")
	}
}

func TestLatestReleaseTokenTrimsWhitespaceAndFallsBackToBuildInfo(t *testing.T) {
	t.Setenv("CPA_TOKEN", "   ")
	oldBuildToken := buildinfo.CPAToken
	buildinfo.CPAToken = "build-token"
	t.Cleanup(func() { buildinfo.CPAToken = oldBuildToken })

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{CPAToken: "\t  \n"},
	}

	if got := latestReleaseToken(cfg); got != "build-token" {
		t.Fatalf("latestReleaseToken with blank env/config = %q, want %q", got, "build-token")
	}
}

func TestGetLatestVersionNilHandlerDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(http.MethodGet, "/latest-version", nil)
	cancelledCtx, cancel := context.WithCancel(req.Context())
	cancel()
	ctx.Request = req.WithContext(cancelledCtx)

	var h *Handler
	h.GetLatestVersion(ctx)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("GetLatestVersion status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}
