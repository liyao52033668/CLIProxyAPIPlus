package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// statusBearingError is a test helper carrying an upstream HTTP status code.
type statusBearingError struct {
	status int
	msg    string
}

func (e *statusBearingError) Error() string { return e.msg }

func (e *statusBearingError) StatusCode() int { return e.status }

// Structured model_not_found responses must cool down the (credential, model)
// pair instead of being treated as caller request faults (#5635).

func TestCodexStructuredModelNotFound_ClassificationAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	testCases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "status 400 with invalid_request_error and model_not_found code",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
		},
		{
			name:   "status 404 with invalid_request_error and model_not_found code",
			status: http.StatusNotFound,
			body:   `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rawErr := &statusBearingError{
				status: tc.status,
				msg:    tc.body,
			}

			if isRequestInvalidError(rawErr) {
				t.Fatalf("isRequestInvalidError(%v) = true, want false (model_not_found is not a caller request fault)", rawErr)
			}

			resultErr := newErrorFromExecution(rawErr)
			if resultErr == nil {
				t.Fatal("newErrorFromExecution returned nil")
			}
			if !isExplicitModelNotFoundError(resultErr) {
				t.Fatalf("expected result error to be classified as explicit model_not_found: %#v", resultErr)
			}
			if resultErr.IsRequestScoped() {
				t.Fatal("resultErr must not be marked request-scoped")
			}

			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-codex-1", Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "gpt-5.5"
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    resultErr,
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("expected auth to be registered")
			}
			state := updated.ModelStates[model]
			if state == nil || !state.Unavailable {
				t.Fatalf("expected model state to be unavailable, got %#v", state)
			}
			remaining := time.Until(state.NextRetryAfter)
			if remaining < 11*time.Hour || remaining > 13*time.Hour {
				t.Fatalf("expected ~12h cooldown, got remaining=%v", remaining)
			}
		})
	}
}

func TestCodexModelNotFound_CallerInputErrorNotModelCooldown(t *testing.T) {
	rawErr := &statusBearingError{
		status: http.StatusBadRequest,
		msg:    `{"error":{"type":"invalid_request_error","message":"The model not found in request body"}}`,
	}
	if !isRequestInvalidError(rawErr) {
		t.Fatalf("isRequestInvalidError(%v) = false, want true for caller request fault", rawErr)
	}
}

func TestCodexModelNotFound_Generic404NotModelNotFound(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	rawErr := &statusBearingError{
		status: http.StatusNotFound,
		msg:    `{"error":{"message":"Not Found"}}`,
	}
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-codex-generic-404", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-5.5"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    newErrorFromExecution(rawErr),
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates[model]
	if state != nil && state.LastError != nil && state.LastError.Code == "model_not_found" {
		t.Fatalf("generic 404 should not have model_not_found error code, got %v", state.LastError.Code)
	}
	if state != nil {
		if remaining := time.Until(state.NextRetryAfter); remaining > 13*time.Hour || remaining < -time.Minute {
			t.Fatalf("generic 404 should not apply the 12h model cooldown, got %v", remaining)
		}
	}
}

func TestCodexStructuredModelNotFound_DisableCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-codex-disable-cooling",
		Provider: "codex",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	rawErr := &statusBearingError{
		status: http.StatusBadRequest,
		msg:    `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
	}

	model := "gpt-5.5"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    newErrorFromExecution(rawErr),
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestIsExplicitModelNotFoundMessageGuardsRequestBodyPhrases(t *testing.T) {
	if !isExplicitModelNotFoundMessage(`{"error":{"type":"invalid_request_error","code":"model_not_found","message":"missing"}}`) {
		t.Fatal("structured model_not_found body must be recognized")
	}
	if isExplicitModelNotFoundMessage("The model not found in request body") {
		t.Fatal("request-body references must not be model-not-found capability errors")
	}
	if isExplicitModelNotFoundMessage("model `gpt-x` is deprecated") {
		t.Fatal("unrelated messages must not be classified as model_not_found")
	}
}
