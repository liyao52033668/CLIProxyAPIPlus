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
