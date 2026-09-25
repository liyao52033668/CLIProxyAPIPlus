package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type authFilesCooldownResponse struct {
	ObservedAt time.Time `json:"observed_at"`
	Files      []struct {
		ID             string          `json:"id"`
		AuthIndex      string          `json:"auth_index"`
		Name           string          `json:"name"`
		Status         string          `json:"status"`
		StatusMessage  string          `json:"status_message"`
		Unavailable    bool            `json:"unavailable"`
		NextRetryAfter time.Time       `json:"next_retry_after"`
		Cooldowns      json.RawMessage `json:"cooldowns"`
	} `json:"files"`
}

func requestAuthFilesCooldowns(t *testing.T, h *Handler, query string) authFilesCooldownResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files"+query, nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload authFilesCooldownResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if payload.ObservedAt.IsZero() || payload.ObservedAt.Location() != time.UTC {
		t.Fatalf("invalid observed_at: %v", payload.ObservedAt)
	}
	return payload
}

func findAuthFilesCooldownEntry(t *testing.T, payload authFilesCooldownResponse, id string) (index int) {
	t.Helper()
	for i, file := range payload.Files {
		if file.ID == id {
			return i
		}
	}
	t.Fatalf("auth file %q not found in response with %d files", id, len(payload.Files))
	return -1
}

func TestListAuthFilesCooldownSnapshotAndObservedAt(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	next := now.Add(time.Hour)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	for _, id := range []string{"a", "b"} {
		if _, err := manager.Register(context.Background(), &coreauth.Auth{
			ID: id, Index: "index-" + id, Provider: "codex", Status: coreauth.StatusError,
			Unavailable: true, NextRetryAfter: next,
			Attributes: map[string]string{"runtime_only": "true"},
			Quota:      coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, ObservedAt: now},
			ModelStates: map[string]*coreauth.ModelState{
				"model-a": {
					Unavailable: true, NextRetryAfter: next,
					Quota: coreauth.QuotaState{
						Exceeded: true, Reason: "quota", NextRecoverAt: next,
						BackoffLevel: 6, ObservedAt: now,
					},
					LastError: &coreauth.Error{HTTPStatus: 429, Message: "private upstream body"},
				},
			},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	beforeA, _ := manager.GetByID("a")
	beforeB, _ := manager.GetByID("b")

	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	for range 2 {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
		h.ListAuthFiles(ctx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}

		var payload struct {
			ObservedAt time.Time `json:"observed_at"`
			Files      []struct {
				ID        string          `json:"id"`
				AuthIndex string          `json:"auth_index"`
				Cooldowns json.RawMessage `json:"cooldowns"`
			} `json:"files"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ObservedAt.IsZero() || payload.ObservedAt.Location() != time.UTC {
			t.Fatalf("invalid observed_at: %v", payload.ObservedAt)
		}
		if len(payload.Files) != 2 {
			t.Fatalf("files = %+v", payload.Files)
		}
		for i, file := range payload.Files {
			wantID := []string{"a", "b"}[i]
			if file.ID != wantID {
				t.Fatalf("identity/order changed: %+v", file)
			}
			var views []coreauth.CooldownView
			if err := json.Unmarshal(file.Cooldowns, &views); err != nil {
				t.Fatal(err)
			}
			if len(views) != 1 || views[0].Scope != "model" || views[0].ModelKey != "model-a" || views[0].Reason != "quota" || views[0].HTTPStatus != 429 {
				t.Fatalf("unexpected cooldowns: %s", file.Cooldowns)
			}
			if views[0].BackoffLevel == nil || *views[0].BackoffLevel != 6 {
				t.Fatalf("backoff level missing or wrong: %+v", views[0])
			}
		}
	}

	afterA, _ := manager.GetByID("a")
	afterB, _ := manager.GetByID("b")
	if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) {
		t.Fatal("GET mutated auth state")
	}
}

func TestListAuthFiles_ExpiredCooldownReconciledToActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Credential-level expired cooldown (e.g. Codex quota exhaustion in Issue 5964)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "codex-expired",
		Index:          "idx-codex-expired",
		FileName:       "codex-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "credential_quota",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
			ObservedAt:    expiredDeadline,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register codex-expired: %v", errRegister)
	}

	// Model-level expired cooldown where all model cooldowns have elapsed
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "model-expired",
		Index:          "idx-model-expired",
		FileName:       "model-expired.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "rate limit exceeded",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		ModelStates: map[string]*coreauth.ModelState{
			"claude-3-5-sonnet": {
				Status:         coreauth.StatusError,
				StatusMessage:  "rate limit exceeded",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register model-expired: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")

	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(payload.Files))
	}

	for _, file := range payload.Files {
		if file.Unavailable {
			t.Errorf("auth %s: expected unavailable=false after cooldown expiration, got true", file.ID)
		}
		if file.Status != string(coreauth.StatusActive) {
			t.Errorf("auth %s: expected status=%q after cooldown expiration, got %q", file.ID, coreauth.StatusActive, file.Status)
		}
		if file.StatusMessage != "" {
			t.Errorf("auth %s: expected empty status_message after cooldown expiration, got %q", file.ID, file.StatusMessage)
		}
		if !file.NextRetryAfter.IsZero() {
			t.Errorf("auth %s: expected zero next_retry_after after cooldown expiration, got %v", file.ID, file.NextRetryAfter)
		}
		if string(file.Cooldowns) != "[]" {
			t.Errorf("auth %s: expected cooldowns=[], got %s", file.ID, file.Cooldowns)
		}
	}
}

func TestListAuthFiles_ExpiredCooldownWithSubsequentTokenFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth auth that had a cooldown, but subsequently had its access token expire / refresh fail.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "codex-token-expired",
		Index:          "idx-codex-token-expired",
		FileName:       "codex-token-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "token expired",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register codex-token-expired: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "codex-token-expired")
	file := payload.Files[idx]

	if !file.Unavailable {
		t.Error("expected unavailable=true for expired token failure, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q for expired token failure, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "token expired" {
		t.Errorf("expected status_message=%q, got %q", "token expired", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_PartialModelCooldownWithAuthFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Auth has an independent auth error ("unauthorized"), but only one of its models is in cooldown.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "auth-unauthorized-partial-model",
		Index:         "idx-auth-unauthorized",
		FileName:      "auth-unauthorized.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: "unauthorized",
		Unavailable:   true,
		LastError:     &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-cool": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-free": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-unauthorized-partial-model: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-unauthorized-partial-model")
	file := payload.Files[idx]

	if !file.Unavailable {
		t.Error("expected unavailable=true due to unauthorized error, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "unauthorized" {
		t.Errorf("expected status_message=%q, got %q", "unauthorized", file.StatusMessage)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_NoCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose access token is still valid (+48h), but background refresh failed with 401
	// and scheduled a retry backoff. The credential itself is still active and usable.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:               "auth-valid-token-refresh-401",
		Index:            "idx-valid-token",
		FileName:         "auth-valid-token.json",
		Provider:         "codex",
		Status:           coreauth.StatusActive,
		Unavailable:      false,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-valid-token-refresh-401: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-valid-token-refresh-401")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false for valid token with scheduled refresh retry, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_ExpiredCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	expiredCooldown := now.Add(-10 * time.Minute)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential with a valid access token (+48h) that previously entered quota cooldown (now expired),
	// and has a pending refresh retry after a 401 refresh error.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:               "auth-valid-token-expired-cooldown",
		Index:            "idx-valid-token-exp-cool",
		FileName:         "auth-valid-token-exp-cool.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredCooldown,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredCooldown,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-valid-token-expired-cooldown: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-valid-token-expired-cooldown")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false after quota cooldown expires for valid token, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
	if file.StatusMessage != "" {
		t.Errorf("expected empty status_message, got %q", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_ModelLevel403CooldownExpired(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth whose model failed with 403 (forbidden) and the conductor copied the error to auth-level,
	// but the model-level cooldown has now elapsed.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "auth-model-403-expired",
		Index:          "idx-model-403-exp",
		FileName:       "auth-model-403-exp.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "forbidden",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-model-403-expired: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-model-403-expired")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false after model-level 403 cooldown expires, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
	if file.StatusMessage != "" {
		t.Errorf("expected empty status_message after cooldown expires, got %q", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_SingleModel403Cooling_OtherModelActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A is in a 403 cooldown, but Model B is active and healthy.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "auth-single-403-other-active",
		Index:         "idx-single-403",
		FileName:      "auth-single-403.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "forbidden",
		Unavailable:   false,
		LastError:     &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-forbidden": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
			"model-working": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-single-403-other-active: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-single-403-other-active")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false because model-working is active, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q because model-working is active, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ExpiredCooldown_ExpiredToken_InFlightRefresh(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)
	inFlightRefresh := now.Add(1 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose quota cooldown expired, but its access token is expired,
	// and a background refresh is currently in-flight/scheduled (NextRefreshAfter in future).
	// Because the access token is expired, selector blocks it and it must NOT be reported as active.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:               "auth-expired-cooldown-expired-token",
		Index:            "idx-exp-cool-exp-tok",
		FileName:         "auth-exp-cool-exp-tok.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredDeadline,
		NextRefreshAfter: inFlightRefresh,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-expired-cooldown-expired-token: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-expired-cooldown-expired-token")
	file := payload.Files[idx]

	if !file.Unavailable {
		t.Error("expected unavailable=true because access token is expired, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "credential_quota" {
		t.Errorf("expected status_message=%q, got %q", "credential_quota", file.StatusMessage)
	}
}

func TestListAuthFiles_ActiveAuth_InactiveFutureTimestamp(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	inactiveFutureTimestamp := now.Add(1 * time.Hour)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An active, healthy auth that has an inactive future NextRetryAfter timestamp
	// (Unavailable=false, Quota.Exceeded=false). Matching selector.availabilityBlock,
	// an inactive timestamp does not block the credential or report it as in error.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "auth-active-inactive-future-timestamp",
		Index:          "idx-active-inactive-future",
		FileName:       "auth-active-future.json",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Unavailable:    false,
		NextRetryAfter: inactiveFutureTimestamp,
		Attributes:     map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-active-inactive-future-timestamp: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-active-inactive-future-timestamp")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false for active auth with inactive future timestamp, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ModelCooling_OtherModelPermanentBlocked(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Model A is in active cooldown (+30m).
	// Model B has a permanent failure with no recovery time (Unavailable=true, NextRetryAfter=zero).
	// Because all schedulable models are blocked, the credential as a whole cannot serve any model.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "auth-all-blocked-cooling-and-perm",
		Index:         "idx-all-blocked",
		FileName:      "auth-all-blocked.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "model failure",
		Unavailable:   true,
		ModelStates: map[string]*coreauth.ModelState{
			"model-cooling": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-perm-blocked": {
				Status:      coreauth.StatusError,
				Unavailable: true,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-all-blocked-cooling-and-perm: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-all-blocked-cooling-and-perm")
	file := payload.Files[idx]

	if !file.Unavailable {
		t.Error("expected unavailable=true because all models are blocked, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q because all models are blocked, got %q", coreauth.StatusError, file.Status)
	}
}

func TestListAuthFiles_SparseModelState_BlockedModelDoesNotMakeAuthUnavailable(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A has an active cooldown in ModelStates, but Model B has no entry in the sparse map yet.
	// Because other supported models are schedulable and auth.Unavailable is false, the credential as a whole
	// must NOT be reported as unavailable.
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:          "auth-sparse-models",
		Index:       "idx-sparse-models",
		FileName:    "auth-sparse-models.json",
		Provider:    "codex",
		Status:      coreauth.StatusActive,
		Unavailable: false,
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	}); errRegister != nil {
		t.Fatalf("register auth-sparse-models: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")
	idx := findAuthFilesCooldownEntry(t, payload, "auth-sparse-models")
	file := payload.Files[idx]

	if file.Unavailable {
		t.Error("expected unavailable=false because sparse unrecorded models remain schedulable, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}
