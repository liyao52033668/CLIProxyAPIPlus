package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type mockOAuthErrorExecutor struct {
	id           string
	refreshCalls atomic.Int32
	errToReturn  error
}

func (e *mockOAuthErrorExecutor) Identifier() string { return e.id }

func (e *mockOAuthErrorExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *mockOAuthErrorExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.errToReturn != nil {
		return nil, e.errToReturn
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-valid-token"
	return auth, nil
}

func (e *mockOAuthErrorExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// blockingInvalidGrantExecutor simulates an in-flight refresh that fails with
// invalid_grant after the caller releases it, so the manager can observe the
// credential being disabled concurrently with the refresh.
type blockingInvalidGrantExecutor struct {
	id           string
	refreshCalls atomic.Int32
	entered      chan struct{}
	release      chan struct{}
	enterOnce    sync.Once
}

func newBlockingInvalidGrantExecutor(id string) *blockingInvalidGrantExecutor {
	return &blockingInvalidGrantExecutor{
		id:      id,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *blockingInvalidGrantExecutor) Identifier() string { return e.id }

func (e *blockingInvalidGrantExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingInvalidGrantExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *blockingInvalidGrantExecutor) Refresh(_ context.Context, _ *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	e.enterOnce.Do(func() { close(e.entered) })
	<-e.release
	return nil, oauthStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
	}
}

func (e *blockingInvalidGrantExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingInvalidGrantExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type oauthStatusError struct {
	code int
	msg  string
}

func (e oauthStatusError) Error() string   { return fmt.Sprintf("status %d: %s", e.code, e.msg) }
func (e oauthStatusError) StatusCode() int { return e.code }

// Unlike upstream, this fork never refreshes disabled credentials at all:
// refreshAuth skips them before reaching the executor and the auto-refresh
// loop never schedules them. The tests below therefore assert the stronger
// fork behavior that subsumes upstream's "disabled invalid_grant unschedule".

func TestRefreshAuth_DisabledAuth_IsNeverRefreshed(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	// A disabled credential without invalid_grant must not be refreshed either.
	auth := &Auth{
		ID:       "normal-disabled-auth",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	if _, errRefresh := manager.refreshAuth(ctx, auth.ID); errRefresh == nil {
		t.Fatalf("expected refresh of disabled auth to be rejected, got nil error")
	}
	if executor.refreshCalls.Load() != 0 {
		t.Fatalf("executor.Refresh called %d times for disabled auth, want 0", executor.refreshCalls.Load())
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current == nil {
		t.Fatal("auth not found in manager")
	}
	if current.Status != StatusDisabled {
		t.Fatalf("auth status = %v, want StatusDisabled", current.Status)
	}
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
	}
}

func TestRefreshAuth_DisabledInvalidGrant_NeverRetries(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "disabled-invalid-grant",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// Disabled + invalid_grant must never reach the executor.
	if _, errRefresh := manager.refreshAuth(ctx, auth.ID); errRefresh == nil {
		t.Fatalf("expected refresh error for disabled invalid_grant auth, got nil")
	}
	if calls := executor.refreshCalls.Load(); calls != 0 {
		t.Fatalf("executor.Refresh called %d times for disabled+invalid_grant auth, want 0", calls)
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
	}
	if !hasDisabledInvalidGrantFailure(current) {
		t.Fatal("hasDisabledInvalidGrantFailure() = false, want true")
	}
	now := time.Now()
	if _, ok := nextRefreshCheckAt(now, current, 15*time.Minute); ok {
		t.Fatal("nextRefreshCheckAt() ok = true, want false for disabled invalid_grant auth")
	}
	if manager.shouldRefresh(current, now) {
		t.Fatal("shouldRefresh() = true, want false for disabled invalid_grant auth")
	}
	if manager.markRefreshPending(auth.ID, now) {
		t.Fatal("markRefreshPending() = true, want false for disabled invalid_grant auth")
	}
}

func TestRefreshAuth_DisabledDuringInFlightInvalidGrant_PermanentlyUnscheduled(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := newBlockingInvalidGrantExecutor("test-provider")
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "inflight-disabled-invalid-grant",
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = manager.refreshAuth(ctx, auth.ID)
	}()

	// Wait until the executor is inside Refresh, then disable the credential
	// concurrently so the failure handler observes a disabled auth.
	<-executor.entered
	manager.mu.Lock()
	if current := manager.auths[auth.ID]; current != nil {
		current.Disabled = true
		current.Status = StatusDisabled
	}
	manager.mu.Unlock()
	close(executor.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshAuth did not finish")
	}

	if calls := executor.refreshCalls.Load(); calls != 1 {
		t.Fatalf("executor.Refresh called %d times, want 1", calls)
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.Status != StatusDisabled {
		t.Fatalf("auth status = %v, want StatusDisabled", current.Status)
	}
	if current.StatusMessage != "disabled (invalid grant)" {
		t.Fatalf("StatusMessage = %q, want %q", current.StatusMessage, "disabled (invalid grant)")
	}
	if !current.Unavailable {
		t.Fatal("Unavailable = false, want true")
	}
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
	}
	if current.RefreshFailures != 0 {
		t.Fatalf("RefreshFailures = %d, want 0", current.RefreshFailures)
	}
	if !hasDisabledInvalidGrantFailure(current) {
		t.Fatal("hasDisabledInvalidGrantFailure() = false, want true")
	}

	// Subsequent refresh attempts must be blocked without calling the executor.
	if _, errSecond := manager.refreshAuth(ctx, auth.ID); errSecond == nil {
		t.Fatalf("expected second call to fail, got nil")
	}
	if executor.refreshCalls.Load() != 1 {
		t.Fatalf("executor was called again for disabled+invalid_grant, calls=%d", executor.refreshCalls.Load())
	}
}

func TestRefreshAuth_EnabledAuth_InvalidGrant_ExponentialBackoff(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	invalidGrantErr := oauthStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
	}
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: invalidGrantErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "enabled-auth-invalid-grant-exp",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 1st failure: backoff 1m
	t1 := time.Now()
	_, _ = manager.refreshAuth(ctx, auth.ID)
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.Status != StatusError {
		t.Fatalf("attempt 1: status = %v, want StatusError", current.Status)
	}
	if current.RefreshFailures != 1 {
		t.Fatalf("attempt 1: RefreshFailures = %d, want 1", current.RefreshFailures)
	}
	diff1 := current.NextRefreshAfter.Sub(t1)
	if diff1 < 50*time.Second || diff1 > 70*time.Second {
		t.Fatalf("attempt 1: backoff diff = %v, want ~1m", diff1)
	}

	// 2nd failure: backoff 2m
	t2 := time.Now()
	_, _ = manager.refreshAuth(ctx, auth.ID)
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 2 {
		t.Fatalf("attempt 2: RefreshFailures = %d, want 2", current.RefreshFailures)
	}
	diff2 := current.NextRefreshAfter.Sub(t2)
	if diff2 < 110*time.Second || diff2 > 130*time.Second {
		t.Fatalf("attempt 2: backoff diff = %v, want ~2m", diff2)
	}

	// 3rd failure: backoff 4m
	t3 := time.Now()
	_, _ = manager.refreshAuth(ctx, auth.ID)
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 3 {
		t.Fatalf("attempt 3: RefreshFailures = %d, want 3", current.RefreshFailures)
	}
	diff3 := current.NextRefreshAfter.Sub(t3)
	if diff3 < 230*time.Second || diff3 > 250*time.Second {
		t.Fatalf("attempt 3: backoff diff = %v, want ~4m", diff3)
	}
}

func TestManager_AutoRefreshLoop_DisabledAuthWithInvalidGrantNeverRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "disabled-invalid-grant-loop",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	manager.StartAutoRefresh(ctx, 10*time.Millisecond)
	defer manager.StopAutoRefresh()

	// Wait 100ms to allow refresh loop cycles
	time.Sleep(100 * time.Millisecond)

	if calls := executor.refreshCalls.Load(); calls != 0 {
		t.Fatalf("executor.Refresh called %d times for disabled+invalid_grant auth, want 0", calls)
	}
}

func TestRefreshAuth_RawFmtError_RecognizesInvalidGrant(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// Error created via fmt.Errorf without implementing StatusCode()
	rawErr := fmt.Errorf("oauth token refresh failed: invalid_grant: account checkpoint required")
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: rawErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "raw-fmt-invalid-grant",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	_, _ = manager.refreshAuth(ctx, auth.ID)
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current.RefreshFailures != 1 {
		t.Fatalf("RefreshFailures = %d, want 1 (should recognize raw fmt.Errorf invalid_grant)", current.RefreshFailures)
	}
	diff := current.NextRefreshAfter.Sub(now)
	if diff < 50*time.Second || diff > 70*time.Second {
		t.Fatalf("backoff diff = %v, want ~1m", diff)
	}
}

func TestNextRefreshCheckAt_DisabledInvalidGrantUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)

	// A disabled credential carrying an invalid_grant failure must stay
	// permanently unscheduled (this fork unschedules every disabled credential).
	invalidGrantDisabledAuth := &Auth{
		ID:       "invalid-grant-disabled",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}
	if _, ok := nextRefreshCheckAt(now, invalidGrantDisabledAuth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false for disabled auth with invalid_grant")
	}
	if !hasDisabledInvalidGrantFailure(invalidGrantDisabledAuth) {
		t.Fatal("hasDisabledInvalidGrantFailure() = false, want true")
	}
}

func TestInvalidGrantBackoffDuration(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, time.Minute},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute},
		{20, 30 * time.Minute},
	}
	for _, tc := range cases {
		if got := invalidGrantBackoffDuration(tc.failures); got != tc.want {
			t.Fatalf("invalidGrantBackoffDuration(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}
