package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexInspectionActionForUsagePrefersWeeklyWindow(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHour := 92
	weeklySafe := 40

	// Weekly safe + 5h exceeded: still suggest disable (added on top of original weekly preference).
	action, reason := codexInspectionActionForUsage(false, &fiveHour, &weeklySafe, settings)
	if action != codexinspection.ActionDisable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDisable)
	}
	if reason != "fiveHourUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "fiveHourUsedPercent >= 85")
	}

	// Weekly exceeded has highest priority and suggests delete.
	weeklyExceeded := 88
	action, reason = codexInspectionActionForUsage(false, &fiveHour, &weeklyExceeded, settings)
	if action != codexinspection.ActionDelete {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDelete)
	}
	if reason != "weeklyUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "weeklyUsedPercent >= 85")
	}
}

func TestCodexInspectionActionForUsageFallsBackToFiveHourWindow(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHourExceeded := 90

	action, reason := codexInspectionActionForUsage(false, &fiveHourExceeded, nil, settings)
	if action != codexinspection.ActionDisable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDisable)
	}
	if reason != "fiveHourUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "fiveHourUsedPercent >= 85")
	}

	// Missing weekly window must not enable, even if 5h has recovered.
	fiveHourSafe := 30
	action, reason = codexInspectionActionForUsage(true, &fiveHourSafe, nil, settings)
	if action != codexinspection.ActionKeep {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionKeep)
	}
	if reason != "no issue detected" {
		t.Fatalf("reason = %q, want %q", reason, "no issue detected")
	}
}

func TestCodexInspectionActionForUsageEnablesOnlyWhenBothWindowsRecovered(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHourExceeded := 99
	weeklySafe := 40
	fiveHourSafe := 20

	// 5h still high + weekly recovered → keep disabled.
	action, reason := codexInspectionActionForUsage(true, &fiveHourExceeded, &weeklySafe, settings)
	if action != codexinspection.ActionKeep {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionKeep)
	}
	if reason != "no issue detected" {
		t.Fatalf("reason = %q, want %q", reason, "no issue detected")
	}

	// Weekly exceeded still suggests delete for disabled accounts.
	weeklyExceeded := 90
	action, reason = codexInspectionActionForUsage(true, &fiveHourExceeded, &weeklyExceeded, settings)
	if action != codexinspection.ActionDelete {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDelete)
	}
	if reason != "weeklyUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "weeklyUsedPercent >= 85")
	}

	// Both recovered → enable.
	action, reason = codexInspectionActionForUsage(true, &fiveHourSafe, &weeklySafe, settings)
	if action != codexinspection.ActionEnable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionEnable)
	}
	if reason != "fiveHourUsedPercent < 85 && weeklyUsedPercent < 85" {
		t.Fatalf("reason = %q, want both windows recovered", reason)
	}
}

func TestCodexInspectionUsagePercentsPrefersWeeklyForUsedPercent(t *testing.T) {
	payload := &quota.CodexUsagePayload{
		RateLimit: &quota.CodexRateLimitInfo{
			PrimaryWindow:   &quota.CodexUsageWindow{UsedPercent: 92, LimitWindowSeconds: codexInspectionFiveHourSeconds},
			SecondaryWindow: &quota.CodexUsageWindow{UsedPercent: 48, LimitWindowSeconds: codexInspectionWeekSeconds},
		},
	}

	fiveHour, weekly, usedPercent, ok := codexInspectionUsagePercents(payload)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if fiveHour == nil || *fiveHour != 92 {
		t.Fatalf("fiveHour = %v, want 92", fiveHour)
	}
	if weekly == nil || *weekly != 48 {
		t.Fatalf("weekly = %v, want 48", weekly)
	}
	if usedPercent == nil || *usedPercent != 48 {
		t.Fatalf("usedPercent = %v, want 48", usedPercent)
	}
}

func TestProviderInspectionRefreshesOnceAndPersistsMetadata(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "claude"}
	executor.refresh = func(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
		executor.calls++
		auth.Metadata["access_token"] = "refreshed-token"
		return auth, nil
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "old-token"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	settings := codexinspection.DefaultSettings()
	settings.Retries = 3
	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "claude-auth", FileName: "claude.json", Provider: "claude"},
		settings,
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)

	if result.Error != "" || result.Action != codexinspection.ActionKeep {
		t.Fatalf("result = %+v, want successful keep", result)
	}
	if executor.calls != 1 {
		t.Fatalf("Refresh calls = %d, want 1", executor.calls)
	}
	updated, ok := manager.GetByID("claude-auth")
	if !ok || updated.Metadata["access_token"] != "refreshed-token" {
		t.Fatalf("updated auth = %+v, want refreshed token", updated)
	}
}

func TestProviderInspectionRejectsNoOpRefreshAsUnsupported(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "noop"}
	executor.refresh = func(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
		executor.calls++
		return auth, nil
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "noop-auth",
		Provider: "noop",
		Metadata: map[string]any{"access_token": "token"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "noop-auth", Provider: "noop"},
		codexinspection.DefaultSettings(),
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)

	if result.Action != codexinspection.ActionFailed || result.Error != "provider inspection unsupported: refresh did not validate credentials" {
		t.Fatalf("result = %+v, want unsupported failure", result)
	}
	if executor.calls != 1 {
		t.Fatalf("Refresh calls = %d, want 1", executor.calls)
	}
}

func TestProviderInspectionPreservesConcurrentAuthUpdates(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "claude"}
	executor.refresh = func(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
		current, ok := manager.GetByID(auth.ID)
		if !ok {
			t.Fatal("auth missing during refresh")
		}
		current.Disabled = true
		current.Status = coreauth.StatusDisabled
		current.Metadata["note"] = "updated concurrently"
		if _, err := manager.Update(ctx, current); err != nil {
			t.Fatalf("concurrent Update: %v", err)
		}
		auth.Metadata["access_token"] = "refreshed-token"
		return auth, nil
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "old-token"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "claude-auth", Provider: "claude"},
		codexinspection.DefaultSettings(),
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)
	if result.Error != "" {
		t.Fatalf("result = %+v, want successful refresh", result)
	}
	updated, ok := manager.GetByID("claude-auth")
	if !ok {
		t.Fatal("updated auth missing")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("updated auth state = %+v, want concurrent disable preserved", updated)
	}
	if updated.Metadata["note"] != "updated concurrently" || updated.Metadata["access_token"] != "refreshed-token" {
		t.Fatalf("updated metadata = %+v, want merged concurrent and refreshed values", updated.Metadata)
	}
}

func TestProviderInspectionUsesDefaultTimeoutWhenSettingIsZero(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "claude"}
	executor.refresh = func(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("Refresh context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > defaultCodexInspectionTimeout {
			t.Fatalf("Refresh deadline remaining = %v, want within %v", remaining, defaultCodexInspectionTimeout)
		}
		auth.Metadata["access_token"] = "refreshed-token"
		return auth, nil
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "old-token"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	settings := codexinspection.DefaultSettings()
	settings.TimeoutSeconds = 0
	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "claude-auth", Provider: "claude"},
		settings,
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)
	if result.Error != "" {
		t.Fatalf("result = %+v, want successful refresh", result)
	}
}

func TestProviderInspectionMarksUnauthorizedAsReauthWithoutRetry(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "claude"}
	executor.refresh = func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
		executor.calls++
		return nil, inspectionTestStatusError{code: http.StatusUnauthorized}
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	settings := codexinspection.DefaultSettings()
	settings.Retries = 3
	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "claude-auth", FileName: "claude.json", Provider: "claude"},
		settings,
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)

	if result.Action != codexinspection.ActionReauth || result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("result = %+v, want 401 reauth", result)
	}
	if executor.calls != 1 {
		t.Fatalf("Refresh calls = %d, want 1", executor.calls)
	}
}

func TestProviderInspectionMarksUnclassifiedErrorsAsFailed(t *testing.T) {
	tests := []struct {
		name       string
		refreshErr error
		statusCode int
	}{
		{name: "without status code", refreshErr: errors.New("temporary refresh failure")},
		{name: "unmapped HTTP error", refreshErr: inspectionTestStatusError{code: http.StatusInternalServerError}, statusCode: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			executor := &inspectionRefreshExecutor{provider: "claude"}
			executor.refresh = func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
				return nil, test.refreshErr
			}
			manager.RegisterExecutor(executor)
			if _, err := manager.Register(context.Background(), &coreauth.Auth{
				ID:       "claude-auth",
				Provider: "claude",
				Metadata: map[string]any{},
			}); err != nil {
				t.Fatalf("Register: %v", err)
			}

			settings := codexinspection.DefaultSettings()
			settings.Retries = 0
			result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
				context.Background(),
				codexinspection.AuthFileRecord{AuthID: "claude-auth", FileName: "claude.json", Provider: "claude"},
				settings,
				codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
			)

			if result.Action != codexinspection.ActionFailed || result.StatusCode != test.statusCode {
				t.Fatalf("result = %+v, want failed with status %d", result, test.statusCode)
			}
		})
	}
}

func TestProviderInspectionStatusCodeActionOverridesDefault(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	executor := &inspectionRefreshExecutor{provider: "claude"}
	executor.refresh = func(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
		executor.calls++
		return nil, inspectionTestStatusError{code: http.StatusUnauthorized}
	}
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	settings := codexinspection.DefaultSettings()
	settings.StatusCodeActions = map[string]map[int]codexinspection.Action{
		"claude": {http.StatusUnauthorized: codexinspection.ActionDelete},
	}
	result := (&codexInspectionProber{manager: manager}).inspectProviderAuth(
		context.Background(),
		codexinspection.AuthFileRecord{AuthID: "claude-auth", FileName: "claude.json", Provider: "Claude"},
		settings,
		codexinspection.InspectionResultItem{Action: codexinspection.ActionKeep, ActionReason: "no issue detected"},
	)

	if result.Action != codexinspection.ActionDelete || result.ActionReason != "401 response" {
		t.Fatalf("result = %+v, want configured delete", result)
	}
}

func TestInspectionNeedsReauth(t *testing.T) {
	unauthorized := inspectionTestStatusError{code: 401}
	if status := inspectionStatusCode(unauthorized); status != 401 {
		t.Fatalf("inspectionStatusCode() = %d, want 401", status)
	}
	if !inspectionNeedsReauth(unauthorized, 401) {
		t.Fatal("inspectionNeedsReauth(401) = false, want true")
	}
	if !inspectionNeedsReauth(inspectionTestStatusError{code: 0, message: "oauth invalid_grant"}, 0) {
		t.Fatal("inspectionNeedsReauth(invalid_grant) = false, want true")
	}
	if inspectionNeedsReauth(inspectionTestStatusError{code: 500}, 500) {
		t.Fatal("inspectionNeedsReauth(500) = true, want false")
	}
}

type inspectionRefreshExecutor struct {
	provider string
	calls    int
	refresh  func(context.Context, *coreauth.Auth) (*coreauth.Auth, error)
}

func (e *inspectionRefreshExecutor) Identifier() string { return e.provider }

func (e *inspectionRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *inspectionRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *inspectionRefreshExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return e.refresh(ctx, auth)
}

func (e *inspectionRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *inspectionRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type inspectionTestStatusError struct {
	code    int
	message string
}

func (e inspectionTestStatusError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "inspection failed"
}

func (e inspectionTestStatusError) StatusCode() int {
	return e.code
}

func TestCodexInspectionUsagePercentsFallsBackByWindowPresence(t *testing.T) {
	payload := &quota.CodexUsagePayload{
		RateLimit: &quota.CodexRateLimitInfo{
			PrimaryWindow: &quota.CodexUsageWindow{UsedPercent: 77, LimitWindowSeconds: codexInspectionFiveHourSeconds},
		},
	}

	fiveHour, weekly, usedPercent, ok := codexInspectionUsagePercents(payload)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if fiveHour == nil || *fiveHour != 77 {
		t.Fatalf("fiveHour = %v, want 77", fiveHour)
	}
	if weekly != nil {
		t.Fatalf("weekly = %v, want nil", weekly)
	}
	if usedPercent == nil || *usedPercent != 77 {
		t.Fatalf("usedPercent = %v, want 77", usedPercent)
	}
}
