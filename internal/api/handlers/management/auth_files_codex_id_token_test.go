package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesIncludesCodexIDTokenClaimsFromNestedJWT(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-user.json",
		FileName: "codex-user.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": "/tmp/codex-user.json",
		},
		Metadata: map[string]any{
			"type":       "codex",
			"email":      "TabithaAnnabeth3952@outlook.com",
			"plan_type":  "k12",
			"account_id": "255de4a6-96a4-430a-b660-358954424e79",
			"id_token":   "eyJhbGciOiJub25lIiwidHlwIjoiSldUIiwiY3BhX3N5bnRoZXRpYyI6dHJ1ZX0.eyJpc3MiOiJodHRwczovL2F1dGgub3BlbmFpLmNvbS8iLCJzdWIiOiJ1c2VyLUNLcWJEOTVoSDV3VzVad05HZVlHQ25HUSIsImF1ZCI6ImNoYXRncHQyYXBpLWV4cG9ydCIsImlhdCI6MTc4MzA2MzY5MCwiZXhwIjoxNzkwODM3OTE2LCJlbWFpbCI6IlRhYml0aGFBbm5hYmV0aDM5NTJAb3V0bG9vay5jb20iLCJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiMjU1ZGU0YTYtOTZhNC00MzBhLWI2NjAtMzU4OTU0NDI0ZTc5IiwiY2hhdGdwdF91c2VyX2lkIjoidXNlci1DS3FiRDk1aEg1d1c1WndOR2VZR0NuR1EiLCJjaGF0Z3B0X3BsYW5fdHlwZSI6ImsxMiJ9fQ.synthetic",
		},
	}); err != nil {
		t.Fatalf("failed to register codex auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	idToken, ok := payload.Files[0]["id_token"].(map[string]any)
	if !ok {
		t.Fatalf("expected id_token object, got %#v", payload.Files[0]["id_token"])
	}
	if got := idToken["chatgpt_account_id"]; got != "255de4a6-96a4-430a-b660-358954424e79" {
		t.Fatalf("chatgpt_account_id = %#v, want %q", got, "255de4a6-96a4-430a-b660-358954424e79")
	}
	if got := idToken["plan_type"]; got != "k12" {
		t.Fatalf("plan_type = %#v, want %q", got, "k12")
	}
}
