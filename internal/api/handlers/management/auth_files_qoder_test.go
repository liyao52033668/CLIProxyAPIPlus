package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRequestQoderPATToken_SavesAuthRecord(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer qdr_pat_token" {
			t.Fatalf("authorization header = %q, want Bearer qdr_pat_token", got)
		}
		if got := r.Header.Get("Cosy-Version"); got == "" {
			t.Fatal("expected Cosy-Version header")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v3/user/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "user-42",
			"name":  "Qoder User",
			"email": "qoder@example.com",
		})
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/qoder-pat-auth", strings.NewReader(`{"base_url":"`+upstream.URL+`","personal_access_token":"qdr_pat_token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RequestQoderPATToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["status"]; got != "ok" {
		t.Fatalf("status = %#v, want ok", got)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("expected 1 saved auth record, got %d", len(store.items))
	}
	var saved *coreauth.Auth
	for _, item := range store.items {
		saved = item
	}
	if saved == nil {
		t.Fatal("expected saved auth record")
	}
	if saved.Provider != "qoder" {
		t.Fatalf("provider = %q, want qoder", saved.Provider)
	}
	if got := saved.Metadata["auth_method"]; got != "pat" {
		t.Fatalf("auth_method = %#v, want pat", got)
	}
	if got := saved.Metadata["personal_access_token"]; got != "qdr_pat_token" {
		t.Fatalf("personal_access_token = %#v, want qdr_pat_token", got)
	}
	if got := saved.Metadata["uid"]; got != "user-42" {
		t.Fatalf("uid = %#v, want user-42", got)
	}
	if got := saved.Metadata["machine_id"]; got != qoderauth.GeneratePATMachineID("qdr_pat_token") {
		t.Fatalf("machine_id = %#v, want PAT-isolated machine id", got)
	}
}

func TestRequestQoderPATToken_SavesAuthRecordWhenUserStatusRejectsPAT(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer qdr_pat_token" {
			t.Fatalf("authorization header = %q, want Bearer qdr_pat_token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRE","message":"token is not active"}`))
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/qoder-pat-auth", strings.NewReader(`{"base_url":"`+upstream.URL+`","personal_access_token":"qdr_pat_token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RequestQoderPATToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["status"]; got != "ok" {
		t.Fatalf("status = %#v, want ok", got)
	}
	if got := resp["uid"]; got == "" {
		t.Fatal("expected derived uid in response")
	}
	if got := resp["warning"]; got == nil {
		t.Fatal("expected warning when user status probe fails")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("expected 1 saved auth record, got %d", len(store.items))
	}
	var saved *coreauth.Auth
	for _, item := range store.items {
		saved = item
	}
	if saved == nil {
		t.Fatal("expected saved auth record")
	}
	if got := saved.Metadata["auth_method"]; got != "pat" {
		t.Fatalf("auth_method = %#v, want pat", got)
	}
	if got := saved.Metadata["uid"]; got == "" {
		t.Fatal("expected derived uid in saved metadata")
	}
}
