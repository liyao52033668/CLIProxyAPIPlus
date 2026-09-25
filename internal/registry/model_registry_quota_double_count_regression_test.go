package registry

import (
	"testing"
	"time"
)

// A credential-wide quota can mark the same client both quota-exceeded and
// suspended. That unavailable client must be counted only once so a provider
// with a healthy sibling stays listed in every catalog view.
func TestCredentialQuotaAndSuspensionCountedOnce(t *testing.T) {
	r := newTestModelRegistry()
	model := &ModelInfo{ID: "gpt-dq-once", OwnedBy: "openai", Type: "openai"}
	r.RegisterClient("dq-healthy", "codex", []*ModelInfo{model})
	r.RegisterClient("dq-quota-suspended", "codex", []*ModelInfo{model})

	registration := r.models["gpt-dq-once"]
	if registration == nil {
		t.Fatal("model registration missing")
	}
	quotaTime := time.Now().Add(-time.Minute)
	registration.QuotaExceededClients = map[string]*time.Time{"dq-quota-suspended": &quotaTime}
	registration.SuspendedClients = map[string]string{"dq-quota-suspended": "credential_quota"}
	clear(r.availableModelsCache)

	if got := r.GetAvailableModels("openai"); len(got) != 1 {
		t.Errorf("/v1/models hid a model with a healthy provider: got %v", got)
	}
	if got := r.GetAvailableModelsByProvider("codex"); len(got) != 1 {
		t.Errorf("provider catalog hid a model with a healthy provider: got %v", got)
	}
	if got := r.GetModelCount(model.ID); got != 1 {
		t.Errorf("model count with one healthy provider = %d, want 1", got)
	}

	// Once every provider is unavailable the model must disappear again.
	registration.SuspendedClients["dq-healthy"] = "manual"
	clear(r.availableModelsCache)

	if got := r.GetAvailableModels("openai"); len(got) != 0 {
		t.Errorf("catalog should hide a model after all providers become unavailable: %v", got)
	}
	if got := r.GetAvailableModelsByProvider("codex"); len(got) != 0 {
		t.Errorf("provider catalog should hide a model after all providers become unavailable: %v", got)
	}
	if got := r.GetModelCount(model.ID); got != 0 {
		t.Errorf("model count with no healthy provider = %d, want 0", got)
	}
}

// An expired quota window must stop masking the suspension, otherwise a client
// stays hidden after its quota recovers.
func TestExpiredQuotaWindowStillCountsSuspension(t *testing.T) {
	r := newTestModelRegistry()
	model := &ModelInfo{ID: "gpt-dq-expired", OwnedBy: "openai", Type: "openai"}
	r.RegisterClient("dq-only", "codex", []*ModelInfo{model})

	registration := r.models["gpt-dq-expired"]
	if registration == nil {
		t.Fatal("model registration missing")
	}
	staleQuotaTime := time.Now().Add(-2 * modelQuotaExceededWindow)
	registration.QuotaExceededClients = map[string]*time.Time{"dq-only": &staleQuotaTime}
	registration.SuspendedClients = map[string]string{"dq-only": "manual"}
	clear(r.availableModelsCache)

	// The quota entry expired, but the suspension itself remains active, so the
	// client is still unavailable and the model must stay hidden.
	if got := r.GetModelCount(model.ID); got != 0 {
		t.Errorf("model count with active suspension after expired quota = %d, want 0", got)
	}
	if got := r.GetAvailableModelsByProvider("codex"); len(got) != 0 {
		t.Errorf("provider catalog should keep a suspended model hidden: %v", got)
	}
}
