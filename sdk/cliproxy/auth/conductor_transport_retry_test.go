package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func windowsCodexTLSHandshakeError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: fmt.Errorf("tls: TLS handshake: %w", &net.OpError{
			Op:     "read",
			Net:    "tcp",
			Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			Addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2},
			Err:    errors.New("wsarecv: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond."),
		}),
	}
}

func dialRefusedError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
}

func TestManager_ShouldRetryAfterError_RetriesPreHTTPTransportFailure(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 0)
	model := "gpt-transport-retry-" + uuid.NewString()
	authID := "transport-retry-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "windows tls handshake", err: windowsCodexTLSHandshakeError(), want: true},
		{name: "dial refused", err: dialRefusedError(), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "unauthorized", err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "certificate", err: &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: x509.UnknownAuthorityError{}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, shouldRetry := m.shouldRetryAfterError(tc.err, 0, []string{"codex"}, model, time.Minute)
			if shouldRetry != tc.want {
				t.Fatalf("shouldRetryAfterError = %v, want %v", shouldRetry, tc.want)
			}
			if shouldRetry && wait != 0 {
				t.Fatalf("transport retry wait = %v, want 0", wait)
			}
		})
	}
}

func TestManager_ShouldRetryAfterError_TransportRetryHonorsRequestRetryLimit(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 0)
	model := "gpt-transport-limit-" + uuid.NewString()
	authID := "transport-limit-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, shouldRetry := m.shouldRetryAfterError(dialRefusedError(), 0, []string{"codex"}, model, time.Minute); shouldRetry {
		t.Fatal("transport error retried with request_retry=0, want no retry")
	}
}

func TestManager_MarkResult_TransportFailureDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-transport-cooldown-" + uuid.NewString()
	cases := []struct {
		name string
		err  error
	}{
		{name: "typed tls handshake", err: windowsCodexTLSHandshakeError()},
		{name: "connection reset message", err: &Error{Message: "connection reset"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transport-" + uuid.NewString(), Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    newErrorFromExecution(tc.err),
			})
			snap, ok := m.GetByID(auth.ID)
			if !ok {
				t.Fatalf("auth %s not found", auth.ID)
			}
			if snap.Unavailable || !snap.NextRetryAfter.IsZero() {
				t.Fatalf("credential cooled by transport failure: unavailable=%v nextRetry=%v", snap.Unavailable, snap.NextRetryAfter)
			}
			if state := existingModelState(snap, canonicalModelKey(model)); state != nil && !state.NextRetryAfter.IsZero() {
				t.Fatalf("model cooled by transport failure: nextRetry=%v", state.NextRetryAfter)
			}
		})
	}
}

func TestManagerExecute_RetriesPreHTTPTransportFailureWithoutCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(1, time.Minute, 0)
	executor := &transportThenSuccessExecutor{
		identifier: "codex",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager.RegisterExecutor(executor)

	model := "gpt-transport-execute-" + uuid.NewString()
	authID := "codex-transport-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after transport retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
	snap, okGet := manager.GetByID(authID)
	if !okGet {
		t.Fatalf("auth %s not found", authID)
	}
	if snap.Unavailable || !snap.NextRetryAfter.IsZero() {
		t.Fatalf("credential cooled after transport retry: unavailable=%v nextRetry=%v", snap.Unavailable, snap.NextRetryAfter)
	}
}

type transportThenSuccessExecutor struct {
	identifier string
	fail       error

	mu    sync.Mutex
	calls int
}

func (e *transportThenSuccessExecutor) Identifier() string { return e.identifier }

func (e *transportThenSuccessExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *transportThenSuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.recordCall() == 1 {
		return nil, e.fail
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*transportThenSuccessExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *transportThenSuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*transportThenSuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *transportThenSuccessExecutor) recordCall() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.calls
}

func (e *transportThenSuccessExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}
