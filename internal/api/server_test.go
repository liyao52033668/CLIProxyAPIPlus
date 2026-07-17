package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type codexSearchCaptureExecutor struct {
	request *http.Request
	body    []byte
	authIDs []string
}

func (e *codexSearchCaptureExecutor) Identifier() string { return "codex" }

func (e *codexSearchCaptureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *codexSearchCaptureExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexSearchCaptureExecutor) Refresh(_ context.Context, candidate *auth.Auth) (*auth.Auth, error) {
	return candidate, nil
}

func (e *codexSearchCaptureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *codexSearchCaptureExecutor) PrepareRequest(req *http.Request, candidate *auth.Auth) error {
	token, _ := candidate.Metadata["access_token"].(string)
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (e *codexSearchCaptureExecutor) HttpRequest(_ context.Context, selected *auth.Auth, req *http.Request) (*http.Response, error) {
	e.request = req.Clone(req.Context())
	e.authIDs = append(e.authIDs, selected.ID)
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		return nil, errRead
	}
	e.body = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"results":[{"url":"https://example.com"}]}`)),
	}, nil
}

type codexSearchGinContextSelector struct {
	ginContext *gin.Context
}

func (s *codexSearchGinContextSelector) Pick(ctx context.Context, _ string, _ string, _ coreexecutor.Options, auths []*auth.Auth) (*auth.Auth, error) {
	s.ginContext, _ = ctx.Value("gin").(*gin.Context)
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

type codexSearchAPIKeyFirstSelector struct{}

func (codexSearchAPIKeyFirstSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*auth.Auth) (*auth.Auth, error) {
	for _, candidate := range auths {
		if candidate.AuthKind() == auth.AuthKindAPIKey {
			return candidate, nil
		}
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)
	if server.codexWorkerCancel != nil {
		t.Cleanup(server.codexWorkerCancel)
	}
	return server
}

func TestServerStartWithReadySignalsAfterListen(t *testing.T) {
	server := newTestServer(t)
	ready := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.StartWithReady(ready)
	}()

	select {
	case <-ready:
	case errStart := <-errCh:
		t.Fatalf("StartWithReady() returned before ready: %v", errStart)
	case <-time.After(time.Second):
		t.Fatal("StartWithReady() did not signal readiness")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if errStop := server.Stop(ctx); errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}
	if errStart := <-errCh; errStart != nil {
		t.Fatalf("StartWithReady() after Stop() error = %v", errStart)
	}
}

func TestServerStartWithReadyDoesNotSignalWhenListenFails(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer listener.Close()

	server := newTestServer(t)
	server.server.Addr = listener.Addr().String()
	ready := make(chan struct{})
	errStart := server.StartWithReady(ready)
	if errStart == nil {
		t.Fatal("StartWithReady() error = nil, want bind failure")
	}
	select {
	case <-ready:
		t.Fatal("StartWithReady() signaled ready after bind failure")
	default:
	}
}

func TestCodexAlphaSearchForwardsAndSanitizesRequest(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}

	payload := `{"id":"session-123","commands":{"search_query":[{"q":"golang channels"}]},"prompt_cache_key":"cache-123","prompt_cache_retention":"24h"}`
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Session_id", "session-123")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if executor.request == nil {
				t.Fatal("Codex executor did not receive a request")
			}
			if got, want := executor.request.URL.String(), "https://chatgpt.com/backend-api/codex/alpha/search"; got != want {
				t.Fatalf("upstream URL = %q, want %q", got, want)
			}
			var upstreamBody map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(executor.body, &upstreamBody); errUnmarshal != nil {
				t.Fatalf("unmarshal upstream body: %v; body=%s", errUnmarshal, executor.body)
			}
			for _, field := range []string{"prompt_cache_key", "prompt_cache_retention"} {
				if _, exists := upstreamBody[field]; exists {
					t.Fatalf("upstream body contains %s: %s", field, executor.body)
				}
			}
			for _, field := range []string{"id", "commands"} {
				if _, exists := upstreamBody[field]; !exists {
					t.Fatalf("upstream body missing %s: %s", field, executor.body)
				}
			}
			if got := executor.request.Header.Get("Authorization"); got != "Bearer codex-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := executor.request.Header.Get("Chatgpt-Account-Id"); got != "account-123" {
				t.Fatalf("Chatgpt-Account-Id = %q", got)
			}
			if got := executor.request.Header.Get("Session_id"); got != "session-123" {
				t.Fatalf("Session_id = %q", got)
			}
		})
	}
}

func TestCodexAlphaSearchRequiresOAuthCredential(t *testing.T) {
	newServer := func(t *testing.T, credentials ...*auth.Auth) (*Server, *codexSearchCaptureExecutor) {
		t.Helper()
		server := newTestServer(t)
		server.handlers.AuthManager.SetSelector(codexSearchAPIKeyFirstSelector{})
		executor := &codexSearchCaptureExecutor{}
		server.handlers.AuthManager.RegisterExecutor(executor)
		for _, credential := range credentials {
			if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
				t.Fatalf("register Codex auth %s: %v", credential.ID, errRegister)
			}
		}
		return server, executor
	}
	apiKeyCredential := func() *auth.Auth {
		return &auth.Auth{
			ID:         "codex-api-key",
			Provider:   "codex",
			Status:     auth.StatusActive,
			Attributes: map[string]string{auth.AttributeAPIKey: "codex-key"},
		}
	}
	oauthCredential := func() *auth.Auth {
		return &auth.Auth{
			ID:       "codex-oauth",
			Provider: "codex",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": "codex-token"},
		}
	}

	t.Run("mixed credentials", func(t *testing.T) {
		server, executor := newServer(t, apiKeyCredential(), oauthCredential())
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if got := executor.authIDs; len(got) != 1 || got[0] != "codex-oauth" {
			t.Fatalf("selected auth IDs = %v, want [codex-oauth]", got)
		}
	})

	t.Run("API key only", func(t *testing.T) {
		server, executor := newServer(t, apiKeyCredential())
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
		}
		if len(executor.authIDs) != 0 {
			t.Fatalf("selected auth IDs = %v, want none", executor.authIDs)
		}
	})
}

func TestCodexAlphaSearchPassesGinContextToAuthSelection(t *testing.T) {
	server := newTestServer(t)
	selector := &codexSearchGinContextSelector{}
	server.handlers.AuthManager.SetSelector(selector)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search?key=home-query-key", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if selector.ginContext == nil {
		t.Fatal("auth selection did not receive the Gin context required by Home scheduling")
	}
	if got := selector.ginContext.Query("key"); got != "home-query-key" {
		t.Fatalf("Gin query key = %q, want %q", got, "home-query-key")
	}
}

func TestCodexAlphaSearchUsesRequestIDForSessionAffinity(t *testing.T) {
	server := newTestServer(t)
	server.handlers.AuthManager.SetSelector(auth.NewSessionAffinitySelector(&auth.RoundRobinSelector{}))
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	for _, id := range []string{"codex-auth-a", "codex-auth-b"} {
		credential := &auth.Auth{
			ID:       id,
			Provider: "codex",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": id},
		}
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register Codex auth: %v", errRegister)
		}
	}

	for _, payload := range []string{
		`{"id":"session-a"}`,
		`{"id":"session-b"}`,
		`{"id":"session-a"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	if got, want := len(executor.authIDs), 3; got != want {
		t.Fatalf("selected auth count = %d, want %d", got, want)
	}
	if executor.authIDs[0] == executor.authIDs[1] {
		t.Fatalf("different sessions selected the same auth %q", executor.authIDs[0])
	}
	if got, want := executor.authIDs[2], executor.authIDs[0]; got != want {
		t.Fatalf("session-affinity auth = %q, want %q", got, want)
	}
}

func TestCodexAlphaSearchRecordsRequestLog(t *testing.T) {
	server := newTestServer(t)
	server.cfg.RequestLog = true
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}

	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	server.codexAlphaSearch(c)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	rawAPIRequest, okRequest := c.Get("API_REQUEST")
	if !okRequest {
		t.Fatal("API_REQUEST was not captured")
	}
	apiRequest, _ := rawAPIRequest.([]byte)
	if !strings.Contains(string(apiRequest), "https://chatgpt.com/backend-api/codex/alpha/search") {
		t.Fatalf("API_REQUEST missing upstream URL: %q", apiRequest)
	}
	if !strings.Contains(string(apiRequest), `{"query":"GPT-5.6"}`) {
		t.Fatalf("API_REQUEST missing body: %q", apiRequest)
	}
	rawAPIResponse, okResponse := c.Get("API_RESPONSE")
	if !okResponse {
		t.Fatal("API_RESPONSE was not captured")
	}
	apiResponse, _ := rawAPIResponse.([]byte)
	if !strings.Contains(string(apiResponse), `{"results":[{"url":"https://example.com"}]}`) {
		t.Fatalf("API_RESPONSE missing body: %q", apiResponse)
	}
}

func TestCodexInspectionUpdateSettingsReloadsWorker(t *testing.T) {
	repo := codexinspection.NewFileSnapshotRepository(filepath.Join(t.TempDir(), "codex-inspection-latest.json"))
	service := codexinspection.NewService(repo, newCodexInspectionGatewayAdapter(nil), &codexinspection.DefaultProber{})
	worker := codexinspection.NewWorker(service)
	adapter := newCodexInspectionServiceAdapter(repo, service, worker)

	settings := codexinspection.DefaultSettings()
	settings.Schedule.Enabled = true
	settings.Schedule.IntervalMinutes = 7

	snapshot, err := adapter.UpdateSettings(context.Background(), settings)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if !worker.Enabled() {
		t.Fatal("worker.Enabled() = false, want true")
	}
	if !snapshot.Settings.Schedule.Enabled || snapshot.Settings.Schedule.IntervalMinutes != 7 {
		t.Fatalf("snapshot settings = %+v, want enabled interval 7", snapshot.Settings.Schedule)
	}

	loaded, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Settings.Schedule.Enabled || loaded.Settings.Schedule.IntervalMinutes != 7 {
		t.Fatalf("persisted settings = %+v, want enabled interval 7", loaded.Settings.Schedule)
	}
}

func TestCodexInspectionUpdateSettingsClearsResultsWhenProviderChanges(t *testing.T) {
	repo := codexinspection.NewFileSnapshotRepository(filepath.Join(t.TempDir(), "codex-inspection-latest.json"))
	initial := codexinspection.DefaultSnapshot()
	initial.Results = []codexinspection.InspectionResultItem{{
		FileName: "codex.json",
		Provider: "codex",
		Action:   codexinspection.ActionKeep,
	}}
	initial.ActionLogs = []codexinspection.InspectionActionLog{{
		FileName: "codex.json",
		Action:   codexinspection.ActionDisable,
	}}
	initial.Run.Summary = codexinspection.InspectionSummary{TotalFiles: 1, SampledCount: 1, KeepCount: 1}
	if err := repo.Save(context.Background(), initial); err != nil {
		t.Fatalf("Save initial snapshot: %v", err)
	}

	service := codexinspection.NewService(repo, newCodexInspectionGatewayAdapter(nil), &codexinspection.DefaultProber{})
	adapter := newCodexInspectionServiceAdapter(repo, service, nil)
	settings := codexinspection.DefaultSettings()
	settings.TargetType = " Claude "

	snapshot, err := adapter.UpdateSettings(context.Background(), settings)
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if snapshot.Settings.TargetType != "claude" {
		t.Fatalf("TargetType = %q, want claude", snapshot.Settings.TargetType)
	}
	if len(snapshot.Results) != 0 || len(snapshot.ActionLogs) != 0 {
		t.Fatalf("results/logs not cleared: results=%d logs=%d", len(snapshot.Results), len(snapshot.ActionLogs))
	}
	if snapshot.Run.Summary != (codexinspection.InspectionSummary{}) {
		t.Fatalf("summary = %+v, want empty", snapshot.Run.Summary)
	}
}

func TestCodexInspectionUpdateSettingsDoesNotReloadWorkerWhenSaveFails(t *testing.T) {
	repo := &failingSnapshotRepository{}
	service := codexinspection.NewService(repo, newCodexInspectionGatewayAdapter(nil), &codexinspection.DefaultProber{})
	worker := codexinspection.NewWorker(service)
	adapter := newCodexInspectionServiceAdapter(repo, service, worker)

	settings := codexinspection.DefaultSettings()
	settings.Schedule.Enabled = true
	settings.Schedule.IntervalMinutes = 7

	if _, err := adapter.UpdateSettings(context.Background(), settings); err == nil {
		t.Fatal("UpdateSettings error = nil, want save failure")
	}
	if worker.Enabled() {
		t.Fatal("worker.Enabled() = true, want false after save failure")
	}
}

func TestCodexInspectionGetSnapshotUsesServiceReconciliation(t *testing.T) {
	repo := &memorySnapshotRepository{snapshot: codexinspection.LatestSnapshot{
		Run: codexinspection.InspectionRunState{Summary: codexinspection.InspectionSummary{
			TotalFiles:    2,
			SampledCount:  2,
			KeepCount:     2,
			DisabledCount: 1,
			EnabledCount:  1,
		}},
		Results: []codexinspection.InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: codexinspection.ActionKeep, Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: codexinspection.ActionKeep, Disabled: true},
		},
	}}
	gateway := &stubCodexInspectionGateway{files: []codexinspection.AuthFileRecord{
		{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false},
		{FileName: "beta.json", DisplayName: "Beta", Disabled: false},
	}}
	service := codexinspection.NewService(repo, gateway, &codexinspection.DefaultProber{})
	adapter := newCodexInspectionServiceAdapter(repo, service, nil)

	snapshot, err := adapter.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.Run.Summary.DisabledCount != 0 {
		t.Fatalf("Summary.DisabledCount = %d, want 0", snapshot.Run.Summary.DisabledCount)
	}
	if snapshot.Run.Summary.EnabledCount != 2 {
		t.Fatalf("Summary.EnabledCount = %d, want 2", snapshot.Run.Summary.EnabledCount)
	}
	if snapshot.Results[1].Disabled {
		t.Fatal("snapshot beta Disabled = true, want false")
	}
}

type failingSnapshotRepository struct{}

type memorySnapshotRepository struct {
	snapshot codexinspection.LatestSnapshot
}

type stubCodexInspectionGateway struct {
	files []codexinspection.AuthFileRecord
}

func (r *memorySnapshotRepository) Load(context.Context) (codexinspection.LatestSnapshot, error) {
	return r.snapshot, nil
}

func (r *memorySnapshotRepository) Save(_ context.Context, snapshot codexinspection.LatestSnapshot) error {
	r.snapshot = snapshot
	return nil
}

func (g *stubCodexInspectionGateway) ListAuthFiles(context.Context, string) ([]codexinspection.AuthFileRecord, error) {
	return g.files, nil
}

func (g *stubCodexInspectionGateway) SetDisabled(context.Context, string, bool) error {
	return nil
}

func (g *stubCodexInspectionGateway) DeleteFiles(context.Context, []string) error {
	return nil
}

func (f *failingSnapshotRepository) Load(context.Context) (codexinspection.LatestSnapshot, error) {
	return codexinspection.DefaultSnapshot(), nil
}

func (f *failingSnapshotRepository) Save(context.Context, codexinspection.LatestSnapshot) error {
	return context.DeadlineExceeded
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Status != "ok" {
			t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestManagementUsageRequiresManagementAuthAndPopsArray(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	server := newTestServer(t)

	redisqueue.Enqueue([]byte(`{"id":1}`))
	redisqueue.Enqueue([]byte(`{"id":2}`))

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	missingKeyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyRR, missingKeyReq)
	if missingKeyRR.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d body=%s", missingKeyRR.Code, http.StatusUnauthorized, missingKeyRR.Body.String())
	}

	legacyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage?count=2", nil)
	legacyReq.Header.Set("Authorization", "Bearer test-management-key")
	legacyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(legacyRR, legacyReq)
	if legacyRR.Code != http.StatusNotFound {
		t.Fatalf("legacy usage status = %d, want %d body=%s", legacyRR.Code, http.StatusNotFound, legacyRR.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	authReq.Header.Set("Authorization", "Bearer test-management-key")
	authRR := httptest.NewRecorder()
	server.engine.ServeHTTP(authRR, authReq)
	if authRR.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d body=%s", authRR.Code, http.StatusOK, authRR.Body.String())
	}

	var payload []json.RawMessage
	if errUnmarshal := json.Unmarshal(authRR.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, authRR.Body.String())
	}
	if len(payload) != 2 {
		t.Fatalf("response records = %d, want 2", len(payload))
	}
	for i, raw := range payload {
		var record struct {
			ID int `json:"id"`
		}
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			t.Fatalf("unmarshal record %d: %v", i, errUnmarshal)
		}
		if record.ID != i+1 {
			t.Fatalf("record %d id = %d, want %d", i, record.ID, i+1)
		}
	}

	if remaining := redisqueue.PopOldest(1); len(remaining) != 0 {
		t.Fatalf("remaining queue = %q, want empty", remaining)
	}
}

func TestHomeEnabledHidesManagementEndpointsAndControlPanel(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	server.cfg.Home.Enabled = true

	t.Run("management endpoints return 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})

	t.Run("management control panel returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestModelsWithClientVersionReturnsCodexCatalog(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-client-version-catalog"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{
			ID:            "gpt-5.5",
			Object:        "model",
			Created:       1776902400,
			OwnedBy:       "openai",
			Type:          "openai",
			DisplayName:   "GPT 5.5",
			Description:   "Frontier model for complex coding, research, and real-world work.",
			ContextLength: 272000,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
		{
			ID:            "custom-codex-model-test",
			Object:        "model",
			OwnedBy:       "test",
			Type:          "openai",
			DisplayName:   "Custom Codex Model",
			Description:   "Custom model from registry",
			ContextLength: 123456,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"low", "medium"}},
		},
		{ID: "grok-imagine-image-quality", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "gpt-image-2", Object: "model", OwnedBy: "openai", Type: "openai"},
		{ID: "grok-imagine-image", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "grok-imagine-video", Object: "model", OwnedBy: "xai", Type: "openai"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "claude-cli/1.0")

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Models []map[string]any `json:"models"`
		Object string           `json:"object"`
		Data   []any            `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp.Object != "" || resp.Data != nil {
		t.Fatalf("expected codex catalog format without object/data, got object=%q data=%v", resp.Object, resp.Data)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected codex catalog models")
	}

	var gpt55 map[string]any
	var custom map[string]any
	for _, model := range resp.Models {
		switch slug, _ := model["slug"].(string); slug {
		case "gpt-5.5":
			gpt55 = model
		case "custom-codex-model-test":
			custom = model
		}
	}
	if gpt55 == nil {
		t.Fatal("expected gpt-5.5 codex catalog entry")
	}
	if _, ok := gpt55["minimal_client_version"]; !ok {
		t.Fatal("expected minimal_client_version in codex catalog")
	}
	serviceTiers, ok := gpt55["service_tiers"].([]any)
	if !ok || len(serviceTiers) != 1 {
		t.Fatalf("expected gpt-5.5 priority service tier, got %#v", gpt55["service_tiers"])
	}
	if custom == nil {
		t.Fatal("expected custom model codex catalog entry")
	}
	if got, _ := custom["display_name"].(string); got != "Custom Codex Model" {
		t.Fatalf("custom display_name = %q, want Custom Codex Model", got)
	}
	if got, _ := custom["description"].(string); got != "Custom model from registry" {
		t.Fatalf("custom description = %q, want Custom model from registry", got)
	}
	if got, _ := custom["context_window"].(float64); got != 123456 {
		t.Fatalf("custom context_window = %v, want 123456", custom["context_window"])
	}
	if custom["base_instructions"] != gpt55["base_instructions"] {
		t.Fatal("expected custom model to use gpt-5.5 base_instructions fallback")
	}
	if _, ok := custom["available_in_plans"].([]any); !ok {
		t.Fatalf("expected custom model to use gpt-5.5 available_in_plans fallback, got %#v", custom["available_in_plans"])
	}
	if got, _ := custom["prefer_websockets"].(bool); got {
		t.Fatalf("custom prefer_websockets = %v, want false", custom["prefer_websockets"])
	}
	if _, ok := custom["apply_patch_tool_type"]; ok {
		t.Fatal("expected custom model to omit apply_patch_tool_type")
	}
	if _, ok := custom["upgrade"]; ok {
		t.Fatal("expected custom model to omit upgrade")
	}
	if _, ok := custom["availability_nux"]; ok {
		t.Fatal("expected custom model to omit availability_nux")
	}

	hiddenModels := map[string]bool{
		"grok-imagine-image-quality": false,
		"gpt-image-2":                false,
		"grok-imagine-image":         false,
		"grok-imagine-video":         false,
	}
	for _, model := range resp.Models {
		slug, _ := model["slug"].(string)
		if _, ok := hiddenModels[slug]; !ok {
			continue
		}
		if visibility, _ := model["visibility"].(string); visibility != "hide" {
			t.Fatalf("%s visibility = %q, want hide", slug, visibility)
		}
		hiddenModels[slug] = true
	}
	for slug, found := range hiddenModels {
		if !found {
			t.Fatalf("expected hidden model %s in codex catalog", slug)
		}
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}
