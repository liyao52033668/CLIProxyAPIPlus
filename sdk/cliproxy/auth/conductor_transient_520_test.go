package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_MarkResult_HTTP520_TransientCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    `<html><body><div class="cf-error-details cf-error-520"><h1>Web server is returning an unknown error</h1></div></body></html>`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	// Quota must not be marked as exceeded for a transient origin error.
	if state.Quota.Exceeded {
		t.Fatalf("expected Quota.Exceeded to be false for 520 origin error")
	}
	if state.Quota.Reason != "" {
		t.Fatalf("expected Quota.Reason to be empty for 520 origin error, got %q", state.Quota.Reason)
	}
	if state.LastError == nil || state.LastError.HTTPStatus != 520 {
		t.Fatalf("expected LastError to retain HTTP 520, got %#v", state.LastError)
	}

	// By default, transient errors apply a 1-minute retry window.
	if !state.Unavailable {
		t.Fatalf("expected model to be marked unavailable during transient cooldown")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 45*time.Second || diff > 75*time.Second {
		t.Fatalf("expected default transient cooldown of ~60s, got %v", diff)
	}
}

func TestManager_MarkResult_HTTP520_CustomTransientCooldown(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-custom-cooldown",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	diff := time.Until(state.NextRetryAfter)
	if diff < 3*time.Second || diff > 7*time.Second {
		t.Fatalf("expected custom transient cooldown of ~5s, got %v", diff)
	}
}

func TestManager_MarkResult_HTTP520_DisabledTransientCooldown(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disabled-cooldown",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when transient cooldown disabled, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when cooldown is disabled")
	}
}

func TestManager_MarkResult_HTTP520_DisableCoolingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disable-cooling-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	m.Register(context.Background(), auth)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling is true, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when disable_cooling is true")
	}
}

func TestManager_MarkResult_HTTP520_WithRetryAfterHint(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	hint := 15 * time.Second
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-5.6-sol",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	diff := time.Until(state.NextRetryAfter)
	if diff < 10*time.Second || diff > 20*time.Second {
		t.Fatalf("expected hint-based cooldown of ~15s, got %v", diff)
	}
}

func TestManager_MarkResult_HTTP520_DisabledTransientCooldown_IgnoresHint(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-disabled-with-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	hint := 15 * time.Second
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-5.6-sol",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	state := updated.ModelStates["gpt-5.6-sol"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}

	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when transient cooldown is disabled (-1) despite RetryAfter hint, got %v", state.NextRetryAfter)
	}
	if state.Unavailable {
		t.Fatalf("expected model not to be unavailable when transient cooldown is disabled")
	}
}

func TestManager_MarkResult_AuthLevel520_DisabledTransientCooldown_IgnoresHint(t *testing.T) {
	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-auth-disabled-with-hint",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	hint := 15 * time.Second
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}

	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth NextRetryAfter to be zero when transient cooldown is disabled (-1) despite RetryAfter hint, got %v", updated.NextRetryAfter)
	}
	if updated.Unavailable {
		t.Fatalf("expected auth not to be unavailable when transient cooldown is disabled")
	}
}

func TestManager_MarkResult_HTTP503_TransientCooldown_RespectsDisabledAndHint(t *testing.T) {
	// 1. When disabled (-1), 503 with RetryAfter hint must NOT apply cooldown.
	{
		prevTransient := transientErrorCooldownSeconds.Load()
		transientErrorCooldownSeconds.Store(-1)
		defer transientErrorCooldownSeconds.Store(prevTransient)

		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-test-503-disabled",
			Provider: "claude",
			Status:   StatusActive,
		}
		m.Register(context.Background(), auth)

		hint := 20 * time.Second
		m.MarkResult(context.Background(), Result{
			AuthID:     auth.ID,
			Provider:   "claude",
			Model:      "claude-3-5-sonnet",
			Success:    false,
			RetryAfter: &hint,
			Error: &Error{
				HTTPStatus: 503,
				Message:    "service unavailable",
			},
		})

		updated, ok := m.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("expected auth to be found")
		}
		state := updated.ModelStates["claude-3-5-sonnet"]
		if state == nil {
			t.Fatalf("expected model state to be present")
		}
		if !state.NextRetryAfter.IsZero() {
			t.Fatalf("expected 503 NextRetryAfter to be zero when transient cooldown is disabled (-1), got %v", state.NextRetryAfter)
		}
		if state.Unavailable {
			t.Fatalf("expected model not to be unavailable when transient cooldown is disabled")
		}
	}

	// 2. When enabled (0 = default), 503 with RetryAfter hint must use hint.
	{
		prevTransient := transientErrorCooldownSeconds.Load()
		transientErrorCooldownSeconds.Store(0)
		defer transientErrorCooldownSeconds.Store(prevTransient)

		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-test-503-hint",
			Provider: "claude",
			Status:   StatusActive,
		}
		m.Register(context.Background(), auth)

		hint := 20 * time.Second
		m.MarkResult(context.Background(), Result{
			AuthID:     auth.ID,
			Provider:   "claude",
			Model:      "claude-3-5-sonnet",
			Success:    false,
			RetryAfter: &hint,
			Error: &Error{
				HTTPStatus: 503,
				Message:    "service unavailable",
			},
		})

		updated, ok := m.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("expected auth to be found")
		}
		state := updated.ModelStates["claude-3-5-sonnet"]
		if state == nil {
			t.Fatalf("expected model state to be present")
		}
		diff := time.Until(state.NextRetryAfter)
		if diff < 15*time.Second || diff > 25*time.Second {
			t.Fatalf("expected 503 hint cooldown of ~20s, got %v", diff)
		}
		if !state.Unavailable {
			t.Fatalf("expected model to be unavailable during cooldown")
		}
	}
}

func TestManager_MarkResult_AuthLevel520_SetsTransientError(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-test-520-authlevel",
		Provider: "codex",
		Status:   StatusActive,
	}
	m.Register(context.Background(), auth)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "",
		Success:  false,
		Error: &Error{
			HTTPStatus: 520,
			Message:    "origin error",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be found")
	}
	if updated.Quota.Exceeded {
		t.Fatalf("expected Quota.Exceeded to be false for 520 origin error")
	}
	if updated.Quota.Reason != "" {
		t.Fatalf("expected Quota.Reason to be empty for 520 origin error, got %q", updated.Quota.Reason)
	}
	if updated.StatusMessage != "transient upstream error" {
		t.Fatalf("expected StatusMessage to be 'transient upstream error', got %q", updated.StatusMessage)
	}
	if updated.LastError == nil || updated.LastError.HTTPStatus != 520 {
		t.Fatalf("expected LastError to retain HTTP 520, got %#v", updated.LastError)
	}
	if !updated.Unavailable {
		t.Fatalf("expected auth to be marked unavailable during transient cooldown")
	}
	diff := time.Until(updated.NextRetryAfter)
	if diff < 45*time.Second || diff > 75*time.Second {
		t.Fatalf("expected default transient cooldown of ~60s, got %v", diff)
	}
}
