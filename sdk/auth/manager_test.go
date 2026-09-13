package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type dummyAuthenticator struct {
	provider string
	record   *coreauth.Auth
}

func (d *dummyAuthenticator) Provider() string { return d.provider }

func (d *dummyAuthenticator) Login(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return d.record, nil
}

func (d *dummyAuthenticator) RefreshLead() *time.Duration { return nil }

func TestManagerLogin_MigratesMatchingLegacyClaudeCredential(t *testing.T) {
	authDir := t.TempDir()
	legacyFileName := "claude-user@example.com.json"
	targetFileName := claudeauth.CredentialFileName("user@example.com", "organization-a", "account-a")
	legacyPath := filepath.Join(authDir, legacyFileName)
	targetPath := filepath.Join(authDir, targetFileName)

	existing := map[string]any{
		"type":              "claude",
		"email":             "user@example.com",
		"organization_uuid": "organization-a",
		"account_uuid":      "account-a",
		"access_token":      "old-token",
		"prefix":            "team",
		"proxy_url":         "http://127.0.0.1:8080",
		"disabled":          true,
		"weight":            float64(5),
	}
	raw, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatalf("marshal legacy credential: %v", errMarshal)
	}
	if errWrite := os.WriteFile(legacyPath, raw, 0o600); errWrite != nil {
		t.Fatalf("write legacy credential: %v", errWrite)
	}

	newRecord := &coreauth.Auth{
		ID:       targetFileName,
		FileName: targetFileName,
		Provider: "claude",
		Storage: &claudeauth.ClaudeTokenStorage{
			Type:             "claude",
			Email:            "user@example.com",
			OrganizationUUID: "organization-a",
			AccountUUID:      "account-a",
			AccessToken:      "new-token",
		},
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "user@example.com",
			"organization_uuid": "organization-a",
			"account_uuid":      "account-a",
			"access_token":      "new-token",
		},
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, &dummyAuthenticator{provider: "claude", record: newRecord})

	_, savedPath, errLogin := mgr.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil)
	if errLogin != nil {
		t.Fatalf("Login error: %v", errLogin)
	}
	if savedPath != targetPath {
		t.Fatalf("savedPath = %s, want %s", savedPath, targetPath)
	}
	if _, errStat := os.Stat(legacyPath); !os.IsNotExist(errStat) {
		t.Fatalf("legacy credential still exists or stat failed: %v", errStat)
	}

	savedRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read migrated credential: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal migrated credential: %v", errUnmarshal)
	}
	for key, want := range map[string]any{
		"access_token": "new-token",
		"prefix":       "team",
		"proxy_url":    "http://127.0.0.1:8080",
		"disabled":     true,
		"weight":       float64(5),
	} {
		if got := saved[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestManagerLogin_DoesNotMigrateLegacyClaudeCredentialFromDifferentOrganization(t *testing.T) {
	authDir := t.TempDir()
	legacyFileName := "claude-user@example.com.json"
	targetFileName := claudeauth.CredentialFileName("user@example.com", "organization-b", "shared-account")
	legacyPath := filepath.Join(authDir, legacyFileName)

	existing := map[string]any{
		"type":              "claude",
		"email":             "user@example.com",
		"organization_uuid": "organization-a",
		"account_uuid":      "shared-account",
		"access_token":      "old-token",
		"prefix":            "team",
	}
	raw, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatalf("marshal legacy credential: %v", errMarshal)
	}
	if errWrite := os.WriteFile(legacyPath, raw, 0o600); errWrite != nil {
		t.Fatalf("write legacy credential: %v", errWrite)
	}

	newRecord := &coreauth.Auth{
		ID:       targetFileName,
		FileName: targetFileName,
		Provider: "claude",
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "user@example.com",
			"organization_uuid": "organization-b",
			"account_uuid":      "shared-account",
			"access_token":      "new-token",
		},
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, &dummyAuthenticator{provider: "claude", record: newRecord})

	if _, _, errLogin := mgr.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil); errLogin != nil {
		t.Fatalf("Login error: %v", errLogin)
	}
	if _, errStat := os.Stat(legacyPath); errStat != nil {
		t.Fatalf("different-organization legacy credential was removed: %v", errStat)
	}

	savedRaw, errRead := os.ReadFile(filepath.Join(authDir, targetFileName))
	if errRead != nil {
		t.Fatalf("read new credential: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal new credential: %v", errUnmarshal)
	}
	if _, inherited := saved["prefix"]; inherited {
		t.Fatalf("new credential inherited prefix from a different organization: %#v", saved["prefix"])
	}
}

func TestManagerLogin_MultipleOrganizationsSharingEmailCoexist(t *testing.T) {
	authDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	file1 := claudeauth.CredentialFileName("shared@example.com", "org-team-1", "account-1")
	record1 := &coreauth.Auth{
		ID:       file1,
		FileName: file1,
		Provider: "claude",
		Storage: &claudeauth.ClaudeTokenStorage{
			Type:             "claude",
			Email:            "shared@example.com",
			OrganizationUUID: "org-team-1",
			AccessToken:      "token-team",
		},
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "shared@example.com",
			"organization_uuid": "org-team-1",
		},
	}
	mgr1 := NewManager(store, &dummyAuthenticator{provider: "claude", record: record1})
	_, savedPath1, errLogin1 := mgr1.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil)
	if errLogin1 != nil {
		t.Fatalf("Login 1 error: %v", errLogin1)
	}

	file2 := claudeauth.CredentialFileName("shared@example.com", "org-enterprise-2", "account-1")
	record2 := &coreauth.Auth{
		ID:       file2,
		FileName: file2,
		Provider: "claude",
		Storage: &claudeauth.ClaudeTokenStorage{
			Type:             "claude",
			Email:            "shared@example.com",
			OrganizationUUID: "org-enterprise-2",
			AccessToken:      "token-enterprise",
		},
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "shared@example.com",
			"organization_uuid": "org-enterprise-2",
		},
	}
	mgr2 := NewManager(store, &dummyAuthenticator{provider: "claude", record: record2})
	_, savedPath2, errLogin2 := mgr2.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil)
	if errLogin2 != nil {
		t.Fatalf("Login 2 error: %v", errLogin2)
	}

	if savedPath1 == savedPath2 {
		t.Fatalf("savedPath1 and savedPath2 are identical: %s; expected distinct files", savedPath1)
	}

	records, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("store.List error: %v", errList)
	}
	if len(records) != 2 {
		t.Fatalf("store.List returned %d records, want 2 distinct organization records", len(records))
	}
}
