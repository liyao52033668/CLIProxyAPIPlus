package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Later failure writes must never shorten a still-live cooldown; they may only
// extend it (#5501). A deliberate zero write (disableCooling) still clears.

func newCooldownMonotonicManager(t *testing.T, models ...string) (*Manager, *Auth) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-monotonic-" + models[0], Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	now := time.Now().Unix()
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model, Created: now})
	}
	reg.RegisterClient(auth.ID, auth.Provider, infos)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return m, auth
}

func TestManager_MarkResult_LaterShorterFailureKeepsLongerModelDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	tests := []struct {
		name   string
		second func(m *Manager, authID string)
	}{
		{
			name: "401_then_short_429",
			second: func(m *Manager, authID string) {
				short := 2 * time.Minute
				m.MarkResult(context.Background(), Result{
					AuthID: authID, Provider: "claude", Model: "model-a",
					Success: false, RetryAfter: &short,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "short 429"},
				})
			},
		},
		{
			name: "401_then_transient_500",
			second: func(m *Manager, authID string) {
				m.MarkResult(context.Background(), Result{
					AuthID: authID, Provider: "claude", Model: "model-a",
					Success: false, Error: &Error{HTTPStatus: http.StatusInternalServerError, Message: "transient 500"},
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, auth := newCooldownMonotonicManager(t, "model-a")
			m.MarkResult(context.Background(), Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
				Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "long 401"},
			})
			before := time.Now()
			snap, _ := m.GetByID(auth.ID)
			state := existingModelState(snap, canonicalModelKey("model-a"))
			if state == nil || state.NextRetryAfter.Before(before.Add(25*time.Minute)) {
				t.Fatalf("precondition failed: 401 deadline missing: %+v", state)
			}

			tc.second(m, auth.ID)

			updated, _ := m.GetByID(auth.ID)
			stateAfter := existingModelState(updated, canonicalModelKey("model-a"))
			if stateAfter == nil {
				t.Fatal("model state missing after second writer")
			}
			if stateAfter.NextRetryAfter.Before(before.Add(25 * time.Minute)) {
				t.Fatalf("second writer shortened live deadline to %v", stateAfter.NextRetryAfter.Sub(before))
			}
		})
	}
}

func TestManager_ApplyAuthFailureState_PreservesLongerCredentialDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	tests := []struct {
		name   string
		second *Error
	}{
		{
			name:   "404_then_invalid_grant",
			second: &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_grant"},
		},
		{
			name:   "404_then_payment_required",
			second: &Error{HTTPStatus: http.StatusForbidden, Message: "payment required"},
		},
		{
			name:   "404_then_transient_500",
			second: &Error{HTTPStatus: http.StatusInternalServerError, Message: "internal server error"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, auth := newCooldownMonotonicManager(t, "model-a")
			// Credential-wide 404 (12h deadline).
			m.MarkResult(context.Background(), Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "",
				Success: false, Error: &Error{HTTPStatus: http.StatusNotFound, Message: "credential not found"},
			})
			before := time.Now()
			snap, _ := m.GetByID(auth.ID)
			if !snap.Unavailable || snap.NextRetryAfter.Before(before.Add(11*time.Hour)) {
				t.Fatalf("precondition failed: expected ~12h deadline, got: %v", snap.NextRetryAfter.Sub(before))
			}

			// Follow up with a shorter credential failure.
			m.MarkResult(context.Background(), Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "",
				Success: false, Error: tc.second,
			})

			updated, _ := m.GetByID(auth.ID)
			if updated.NextRetryAfter.Before(before.Add(11 * time.Hour)) {
				t.Fatalf("shorter failure shortened credential-level deadline to %v", updated.NextRetryAfter.Sub(before))
			}
		})
	}
}
