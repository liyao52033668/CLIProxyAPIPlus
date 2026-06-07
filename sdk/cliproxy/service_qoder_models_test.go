package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_QoderStoresDynamicModelsAndContracts(t *testing.T) {
	previousFetchQoderCatalog := fetchQoderCatalog
	fetchQoderCatalog = func(ctx context.Context, auth *coreauth.Auth, cfg *config.Config) executor.QoderModelCatalog {
		if auth == nil {
			t.Fatal("expected auth to be passed to qoder catalog fetcher")
		}
		return executor.QoderModelCatalog{
			Models: []*internalregistry.ModelInfo{{
				ID:          "qoder-dynamic",
				Object:      "model",
				Created:     1,
				OwnedBy:     "qoder",
				Type:        "qoder",
				DisplayName: "Qoder Dynamic",
			}},
			Contracts: map[string]executor.QoderModelContract{
				"qoder-dynamic": {Source: "dynamic-source", IsReasoning: true, AliyunUserType: "pro"},
			},
		}
	}
	defer func() {
		fetchQoderCatalog = previousFetchQoderCatalog
	}()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: "qoder-auth.json", Provider: "qoder", Status: coreauth.StatusActive}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	executor.ClearQoderModelContracts(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
		executor.ClearQoderModelContracts(auth.ID)
	})

	service.registerModelsForAuth(auth)
	models := registry.GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("expected 1 dynamically fetched qoder model, got %d", len(models))
	}
	if models[0].ID != "qoder-dynamic" {
		t.Fatalf("expected dynamic qoder model id %q, got %q", "qoder-dynamic", models[0].ID)
	}
	if _, ok := executor.LoadQoderModelContract(auth.ID, "qoder-dynamic"); !ok {
		t.Fatal("expected dynamic qoder contract to be stored")
	}
}

func TestRegisterModelsForAuth_QoderClearsContractsOnDynamicFallback(t *testing.T) {
	previousFetchQoderCatalog := fetchQoderCatalog
	fetchQoderCatalog = func(ctx context.Context, auth *coreauth.Auth, cfg *config.Config) executor.QoderModelCatalog {
		return executor.QoderModelCatalog{}
	}
	defer func() {
		fetchQoderCatalog = previousFetchQoderCatalog
	}()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: "qoder-auth-fallback.json", Provider: "qoder", Status: coreauth.StatusActive}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	executor.StoreQoderModelContracts(auth.ID, map[string]executor.QoderModelContract{
		"stale-model": {Source: "stale-source"},
	})
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
		executor.ClearQoderModelContracts(auth.ID)
	})

	service.registerModelsForAuth(auth)
	if _, ok := executor.LoadQoderModelContract(auth.ID, "stale-model"); ok {
		t.Fatal("expected stale qoder contract cache to be cleared on fallback")
	}
	models := registry.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected static qoder fallback models to be registered")
	}
}

func TestRegisterModelsForAuth_NonQoderClearsStaleQoderContracts(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: "provider-switch-auth.json", Provider: "gemini", Status: coreauth.StatusActive}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	executor.StoreQoderModelContracts(auth.ID, map[string]executor.QoderModelContract{
		"stale-model": {Source: "stale-source"},
	})
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
		executor.ClearQoderModelContracts(auth.ID)
	})

	service.registerModelsForAuth(auth)
	if _, ok := executor.LoadQoderModelContract(auth.ID, "stale-model"); ok {
		t.Fatal("expected non-qoder registration to clear stale qoder contract cache")
	}
}
