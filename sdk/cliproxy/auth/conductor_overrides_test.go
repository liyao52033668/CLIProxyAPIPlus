package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const requestScopedNotFoundMessage = "Item with id 'rs_0b5f3eb6f51f175c0169ca74e4a85881998539920821603a74' not found. Items are not persisted when `store` is set to false. Try again with `store` set to true, or remove this item from your input."

func TestManager_ShouldRetryAfterError_RespectsAuthRequestRetryOverride(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"request_retry": float64(0),
		},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false for request_retry=0, got true (wait=%v)", wait)
	}

	auth.Metadata["request_retry"] = float64(1)
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	wait, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for request_retry=1, got false")
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}

	_, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 1, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1, got true")
	}
}

func TestManager_ShouldRetryAfterError_UsesOAuthModelAliasForCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"kimi": {
			{Name: "deepseek-v3.1", Alias: "pool-model"},
		},
	})

	routeModel := "pool-model"
	upstreamModel := "deepseek-v3.1"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "kimi",
		ModelStates: map[string]*ModelState{
			upstreamModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 429, Message: "quota"}, 0, []string{"kimi"}, routeModel, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true, got false (wait=%v)", wait)
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}
}

type credentialRetryLimitExecutor struct {
	id string

	mu    sync.Mutex
	calls int
}

func (e *credentialRetryLimitExecutor) Identifier() string {
	return e.id
}

func (e *credentialRetryLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall()
	return nil, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *credentialRetryLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *credentialRetryLimitExecutor) recordCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
}

func (e *credentialRetryLimitExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type authFallbackExecutor struct {
	id string

	mu                sync.Mutex
	executeCalls      []string
	countCalls        []string
	streamCalls       []string
	executeErrors     map[string]error
	countErrors       map[string]error
	streamFirstErrors map[string]error
}

func (e *authFallbackExecutor) Identifier() string {
	return e.id
}

func (e *authFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	err := e.executeErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	err := e.streamFirstErrors[auth.ID]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *authFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authFallbackExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.countCalls = append(e.countCalls, auth.ID)
	err := e.countErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *authFallbackExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *authFallbackExecutor) CountCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countCalls))
	copy(out, e.countCalls)
	return out
}

func (e *authFallbackExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

type retryAfterStatusError struct {
	status     int
	message    string
	retryAfter time.Duration
}

type requestScopedStatusError struct {
	status  int
	message string
}

func (e *requestScopedStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *requestScopedStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *requestScopedStatusError) IsRequestScoped() bool {
	return e != nil
}

func (e *retryAfterStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *retryAfterStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *retryAfterStatusError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	d := e.retryAfter
	return &d
}

func newCredentialRetryLimitTestManager(t *testing.T, maxRetryCredentials int) (*Manager, *credentialRetryLimitExecutor) {
	t.Helper()

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, maxRetryCredentials)

	executor := &credentialRetryLimitExecutor{id: "claude"}
	m.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &Auth{ID: baseID + "-auth-1", Provider: "claude"}
	auth2 := &Auth{ID: baseID + "-auth-2", Provider: "claude"}

	// Auth selection requires that the global model registry knows each credential supports the model.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, errRegister := m.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	return m, executor
}

func TestManager_MaxRetryCredentials_LimitsCrossCredentialRetries(t *testing.T) {
	request := cliproxyexecutor.Request{Model: "test-model"}
	testCases := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(m *Manager) error {
				_, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			limitedManager, limitedExecutor := newCredentialRetryLimitTestManager(t, 1)
			if errInvoke := tc.invoke(limitedManager); errInvoke == nil {
				t.Fatalf("expected error for limited retry execution")
			}
			if calls := limitedExecutor.Calls(); calls != 1 {
				t.Fatalf("expected 1 call with max-retry-credentials=1, got %d", calls)
			}

			unlimitedManager, unlimitedExecutor := newCredentialRetryLimitTestManager(t, 0)
			if errInvoke := tc.invoke(unlimitedManager); errInvoke == nil {
				t.Fatalf("expected error for unlimited retry execution")
			}
			if calls := unlimitedExecutor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=0, got %d", calls)
			}
		})
	}
}

func TestManager_ModelSupportBadRequest_FallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := range 2 {
		resp, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute %d error = %v, want success", i, errExecute)
		}
		if string(resp.Payload) != goodAuth.ID {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), goodAuth.ID)
		}
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManagerExecuteStream_ModelSupportBadRequestFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := range 2 {
		streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute stream %d error = %v, want success", i, errExecute)
		}
		var payload []byte
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("execute stream %d chunk error = %v, want success", i, chunk.Err)
			}
			payload = append(payload, chunk.Payload...)
		}
		if string(payload) != goodAuth.ID {
			t.Fatalf("execute stream %d payload = %q, want %q", i, string(payload), goodAuth.ID)
		}
	}

	got := executor.StreamCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model"
	m.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 500, Message: "boom"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride_On403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-403",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}

	if count := reg.GetModelCount(model); count <= 0 {
		t.Fatalf("expected model count > 0 when disable_cooling=true, got %d", count)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-403-exec": &Error{
				HTTPStatus: http.StatusForbidden,
				Message:    "forbidden",
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-403-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusForbidden {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusForbidden)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusForbidden {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusForbidden)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 2 * time.Minute,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusTooManyRequests {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusTooManyRequests)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusTooManyRequests {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want 2", len(calls))
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_Execute_DisableCooling_RetriesAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 100*time.Millisecond, 0)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-retryafter-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 5 * time.Millisecond,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-retryafter-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-retryafter-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 4 {
		t.Fatalf("execute calls = %d, want 4 (initial + 3 retries)", len(calls))
	}
}

func TestManager_RequestScopedErrorStopsCredentialFallbackWithoutSuspendingAuth(t *testing.T) {
	incompleteErr := &requestScopedStatusError{
		status:  http.StatusRequestTimeout,
		message: "stream error: stream disconnected before completion: stream closed before response.completed",
	}
	invalidRequestErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}`,
	}
	badRequestErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"bad_request_error","code":"invalid_value","message":"Bad input."}}`,
	}
	tests := []struct {
		name       string
		stream     bool
		err        error
		wantStatus int
	}{
		{name: "non-streaming incomplete", err: incompleteErr, wantStatus: http.StatusRequestTimeout},
		{name: "streaming incomplete", stream: true, err: incompleteErr, wantStatus: http.StatusRequestTimeout},
		{name: "non-streaming invalid request", err: invalidRequestErr, wantStatus: http.StatusBadRequest},
		{name: "streaming invalid request", stream: true, err: invalidRequestErr, wantStatus: http.StatusBadRequest},
		{name: "non-streaming bad request", err: badRequestErr, wantStatus: http.StatusBadRequest},
		{name: "streaming bad request", stream: true, err: badRequestErr, wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(2, 30*time.Second, 0)

			executor := &authFallbackExecutor{id: "codex"}
			if tc.stream {
				executor.streamFirstErrors = map[string]error{"aa-bad-auth": tc.err}
			} else {
				executor.executeErrors = map[string]error{"aa-bad-auth": tc.err}
			}
			m.RegisterExecutor(executor)

			model := "gpt-5.5"
			badAuth := &Auth{ID: "aa-bad-auth", Provider: "codex"}
			goodAuth := &Auth{ID: "bb-good-auth", Provider: "codex"}

			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(badAuth.ID, badAuth.Provider, []*registry.ModelInfo{{ID: model}})
			reg.RegisterClient(goodAuth.ID, goodAuth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				reg.UnregisterClient(badAuth.ID)
				reg.UnregisterClient(goodAuth.ID)
			})

			if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
				t.Fatalf("register bad auth: %v", errRegister)
			}
			if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
				t.Fatalf("register good auth: %v", errRegister)
			}

			var errExecute error
			if tc.stream {
				result, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if result != nil {
					for range result.Chunks {
					}
				}
				errExecute = errStream
			} else {
				_, errExecute = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errExecute == nil {
				t.Fatal("expected request-scoped stream error")
			}
			if got := statusCodeFromError(errExecute); got != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got, tc.wantStatus)
			}

			var calls []string
			if tc.stream {
				calls = executor.StreamCalls()
			} else {
				calls = executor.ExecuteCalls()
			}
			if len(calls) != 1 || calls[0] != badAuth.ID {
				t.Fatalf("credential calls = %v, want [%s]", calls, badAuth.ID)
			}

			updatedBad, ok := m.GetByID(badAuth.ID)
			if !ok || updatedBad == nil {
				t.Fatal("expected bad auth to remain registered")
			}
			if updatedBad.Unavailable {
				t.Fatal("expected request-scoped error to keep auth available")
			}
			if !updatedBad.NextRetryAfter.IsZero() {
				t.Fatalf("expected auth cooldown to remain unset, got %v", updatedBad.NextRetryAfter)
			}
			if state := updatedBad.ModelStates[model]; state != nil {
				t.Fatalf("expected request-scoped error to avoid model cooldown state, got %#v", state)
			}
		})
	}
}

func TestManager_DeepSeekCredentialFailuresRotateCredential(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		message   string
		wantQuota bool
	}{
		{
			name:    "authentication failure",
			status:  http.StatusUnauthorized,
			message: `{"error":{"code":"invalid_request_error","message":"Authentication Fails, Your api key: ****heck is invalid","param":null,"type":"authentication_error"}}`,
		},
		{
			name:      "rate limit with generic request error code",
			status:    http.StatusTooManyRequests,
			message:   `{"error":{"code":"invalid_request_error","message":"Rate Limit Reached","param":null,"type":"unknown_error"}}`,
			wantQuota: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(2, 30*time.Second, 0)

			const provider = "openai-compatibility"
			const model = "deepseek-v4-pro"

			executor := &authFallbackExecutor{
				id: provider,
				executeErrors: map[string]error{
					"aa-failed-key": &Error{HTTPStatus: tc.status, Message: tc.message},
				},
			}
			m.RegisterExecutor(executor)

			failedAuth := &Auth{ID: "aa-failed-key", Provider: provider}
			availableAuth := &Auth{ID: "bb-valid-key", Provider: provider}

			reg := registry.GetGlobalRegistry()
			models := []*registry.ModelInfo{{ID: model}}
			reg.RegisterClient(failedAuth.ID, provider, models)
			reg.RegisterClient(availableAuth.ID, provider, models)
			t.Cleanup(func() {
				reg.UnregisterClient(failedAuth.ID)
				reg.UnregisterClient(availableAuth.ID)
			})

			if _, errRegister := m.Register(context.Background(), failedAuth); errRegister != nil {
				t.Fatalf("register failed auth: %v", errRegister)
			}
			if _, errRegister := m.Register(context.Background(), availableAuth); errRegister != nil {
				t.Fatalf("register available auth: %v", errRegister)
			}

			resp, errExecute := m.Execute(
				context.Background(),
				[]string{provider},
				cliproxyexecutor.Request{Model: model},
				cliproxyexecutor.Options{},
			)
			if errExecute != nil {
				t.Fatalf("expected fallback to the next credential, got error: %v", errExecute)
			}
			if got := string(resp.Payload); got != availableAuth.ID {
				t.Fatalf("served by %q, want %q", got, availableAuth.ID)
			}
			wantCalls := []string{failedAuth.ID, availableAuth.ID}
			if calls := executor.ExecuteCalls(); !slices.Equal(calls, wantCalls) {
				t.Fatalf("credential calls = %v, want %v", calls, wantCalls)
			}

			updatedFailed, ok := m.GetByID(failedAuth.ID)
			if !ok || updatedFailed == nil {
				t.Fatal("expected failed auth to remain registered")
			}
			state := updatedFailed.ModelStates[model]
			if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
				t.Fatalf("failed auth model state = %#v, want active cooldown", state)
			}
			if tc.wantQuota && (!state.Quota.Exceeded || state.Quota.Reason != "quota") {
				t.Fatalf("failed auth quota state = %#v, want exceeded quota", state.Quota)
			}
		})
	}
}

func TestManager_DeepSeekInsufficientBalanceRotatesCredentialAndRebindsSession(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(2, 30*time.Second, 0)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	m.SetSelector(affinity)

	const provider = "openai-compatibility"
	const model = "deepseek-v4-pro"

	executor := &authFallbackExecutor{
		id: provider,
		executeErrors: map[string]error{
			"aa-empty-balance": &Error{
				HTTPStatus: http.StatusPaymentRequired,
				Message:    `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`,
			},
		},
	}
	m.RegisterExecutor(executor)

	depletedAuth := &Auth{ID: "aa-empty-balance", Provider: provider}
	availableAuth := &Auth{ID: "bb-available-balance", Provider: provider}

	reg := registry.GetGlobalRegistry()
	models := []*registry.ModelInfo{{ID: model}}
	reg.RegisterClient(depletedAuth.ID, provider, models)
	reg.RegisterClient(availableAuth.ID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(depletedAuth.ID)
		reg.UnregisterClient(availableAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), depletedAuth); errRegister != nil {
		t.Fatalf("register depleted auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), availableAuth); errRegister != nil {
		t.Fatalf("register available auth: %v", errRegister)
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "deepseek-insufficient-balance",
	}}
	beforeExecute := time.Now()
	resp, errExecute := m.Execute(
		context.Background(),
		[]string{provider},
		cliproxyexecutor.Request{Model: model},
		opts,
	)
	if errExecute != nil {
		t.Fatalf("expected fallback to the next credential, got error: %v", errExecute)
	}
	if got := string(resp.Payload); got != availableAuth.ID {
		t.Fatalf("served by %q, want %q", got, availableAuth.ID)
	}

	resp, errExecute = m.Execute(
		context.Background(),
		[]string{provider},
		cliproxyexecutor.Request{Model: model},
		opts,
	)
	if errExecute != nil {
		t.Fatalf("expected rebound session to use the next credential, got error: %v", errExecute)
	}
	if got := string(resp.Payload); got != availableAuth.ID {
		t.Fatalf("rebound session served by %q, want %q", got, availableAuth.ID)
	}
	wantCalls := []string{depletedAuth.ID, availableAuth.ID, availableAuth.ID}
	if calls := executor.ExecuteCalls(); !slices.Equal(calls, wantCalls) {
		t.Fatalf("credential calls = %v, want %v", calls, wantCalls)
	}

	updatedDepleted, ok := m.GetByID(depletedAuth.ID)
	if !ok || updatedDepleted == nil {
		t.Fatal("expected depleted auth to remain registered")
	}
	state := updatedDepleted.ModelStates[model]
	if state == nil {
		t.Fatal("expected the depleted credential to be cooled down for the model")
	}
	if !state.Unavailable {
		t.Fatal("expected the depleted credential to be unavailable for the model")
	}
	if state.NextRetryAfter.Before(beforeExecute.Add(29 * time.Minute)) {
		t.Fatalf("cooldown expires at %v, want approximately 30 minutes", state.NextRetryAfter)
	}
}

// TestManager_UnknownUpstreamErrorRotatesAndPenalizesModelOnly pins the upstream
// 500 "status":"UNKNOWN" contract. It is an upstream internal failure, not a
// request fault, so the request must fall through to the next credential. The
// cooldown that follows must land on the (credential, model) pair only: sibling
// models on the same credential stay selectable.
func TestManager_UnknownUpstreamErrorRotatesAndPenalizesModelOnly(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	const provider = "gemini"
	const model = "gemini-3.6-pro"
	const siblingModel = "gemini-3.6-flash"

	executor := &authFallbackExecutor{id: provider}
	executor.executeErrors = map[string]error{
		"aa-bad-auth": &Error{
			HTTPStatus: http.StatusInternalServerError,
			Message:    `{"error":{"code":500,"message":"Internal error encountered.","status":"UNKNOWN"}}`,
		},
	}
	m.RegisterExecutor(executor)

	badAuth := &Auth{ID: "aa-bad-auth", Provider: provider}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: provider}

	reg := registry.GetGlobalRegistry()
	models := []*registry.ModelInfo{{ID: model}, {ID: siblingModel}}
	reg.RegisterClient(badAuth.ID, provider, models)
	reg.RegisterClient(goodAuth.ID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("expected fallback to the next credential, got error: %v", errExecute)
	}
	if got := string(resp.Payload); got != goodAuth.ID {
		t.Fatalf("served by %q, want %q", got, goodAuth.ID)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != badAuth.ID || calls[1] != goodAuth.ID {
		t.Fatalf("credential calls = %v, want [%s %s]", calls, badAuth.ID, goodAuth.ID)
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatal("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatal("expected the failing (credential, model) pair to be penalized")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected a cooldown on the failing (credential, model) pair")
	}

	now := time.Now()
	if blocked, _, _ := isAuthBlockedForModel(updatedBad, model, now); !blocked {
		t.Fatal("expected the failing model to be blocked on that credential")
	}
	if blocked, reason, _ := isAuthBlockedForModel(updatedBad, siblingModel, now); blocked {
		t.Fatalf("sibling model was blocked on the same credential (reason=%v); the penalty must stay scoped to (credential, model)", reason)
	}
}

func TestManager_MarkResult_RequestScopedNotFoundDoesNotCooldownAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "openai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-4.1"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    requestScopedNotFoundMessage,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid model cooldown state, got %#v", state)
	}
}

func TestManager_RequestScopedNotFoundStopsRetryWithoutSuspendingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    requestScopedNotFoundMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "gpt-4.1"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "openai"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "openai"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected request-scoped not-found error")
	}
	errResult, ok := errExecute.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", errExecute)
	}
	if errResult.HTTPStatus != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", errResult.HTTPStatus, http.StatusNotFound)
	}
	if errResult.Message != requestScopedNotFoundMessage {
		t.Fatalf("message = %q, want %q", errResult.Message, requestScopedNotFoundMessage)
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	if updatedBad.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep bad auth available")
	}
	if !updatedBad.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep bad auth cooldown unset, got %v", updatedBad.NextRetryAfter)
	}
	if state := updatedBad.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid bad auth model cooldown state, got %#v", state)
	}
}

func TestApplyAuthFailureState_RequestScopedRecordsClientFault(t *testing.T) {
	now := time.Now()
	auth := &Auth{ID: "aa-request-scoped", Provider: "claude"}
	resultErr := &Error{
		Code:       requestScopedErrorCode,
		Message:    "request rejected: context_length_exceeded",
		HTTPStatus: http.StatusBadRequest,
	}

	applyAuthFailureState(auth, resultErr, nil, now)

	// The credential must stay healthy: no cooldown, not unavailable.
	if auth.Unavailable {
		t.Fatalf("request-scoped failure marked auth unavailable")
	}
	if auth.Status != StatusActive {
		t.Fatalf("Status = %q, want %q (credential stays healthy)", auth.Status, StatusActive)
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero (no cooldown for request fault)", auth.NextRetryAfter)
	}
	// The client-side fault must be visible to management UIs.
	if got := auth.StatusMessage; !strings.HasPrefix(got, "request fault (client-side): ") {
		t.Fatalf("StatusMessage = %q, want request fault (client-side) prefix", got)
	}
}
