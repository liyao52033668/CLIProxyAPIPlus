package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceApplyCoreAuthAddOrUpdate_DeleteReAddDoesNotInheritStaleRuntimeState(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "service-stale-state-auth"
	modelID := "stale-model"
	lastRefreshedAt := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	nextRefreshAfter := lastRefreshedAt.Add(30 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:               authID,
		Provider:         "claude",
		Status:           coreauth.StatusActive,
		LastRefreshedAt:  lastRefreshedAt,
		NextRefreshAfter: nextRefreshAfter,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {
				Quota: coreauth.QuotaState{BackoffLevel: 7},
			},
		},
	})

	service.applyCoreAuthRemoval(context.Background(), authID)

	disabled, ok := service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after removal")
	}
	if !disabled.Disabled || disabled.Status != coreauth.StatusDisabled {
		t.Fatalf("expected disabled auth after removal, got disabled=%v status=%v", disabled.Disabled, disabled.Status)
	}
	if disabled.LastRefreshedAt.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior LastRefreshedAt for regression setup")
	}
	if disabled.NextRefreshAfter.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior NextRefreshAfter for regression setup")
	}

	// Reconcile prunes unsupported model state during registration, so seed the
	// disabled snapshot explicitly before exercising delete -> re-add behavior.
	disabled.ModelStates = map[string]*coreauth.ModelState{
		modelID: {
			Quota: coreauth.QuotaState{BackoffLevel: 7},
		},
	}
	if _, err := service.coreManager.Update(context.Background(), disabled); err != nil {
		t.Fatalf("seed disabled auth stale ModelStates: %v", err)
	}

	disabled, ok = service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after stale state seeding")
	}
	if len(disabled.ModelStates) == 0 {
		t.Fatalf("expected disabled auth to carry seeded ModelStates for regression setup")
	}

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
	})

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected re-added auth to be present")
	}
	if updated.Disabled {
		t.Fatalf("expected re-added auth to be active")
	}
	if !updated.LastRefreshedAt.IsZero() {
		t.Fatalf("expected LastRefreshedAt to reset on delete -> re-add, got %v", updated.LastRefreshedAt)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("expected NextRefreshAfter to reset on delete -> re-add, got %v", updated.NextRefreshAfter)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected ModelStates to reset on delete -> re-add, got %d entries", len(updated.ModelStates))
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(authID); len(models) == 0 {
		t.Fatalf("expected re-added auth to re-register models in global registry")
	}
}

func TestSyncCodexConfigReconcilesAuthAndModelsBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if errMkdir := os.Mkdir(authDir, 0o700); errMkdir != nil {
		t.Fatalf("create auth dir: %v", errMkdir)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("create config file: %v", errWrite)
	}

	initialCfg := &config.Config{AuthDir: authDir}
	if errSave := config.SaveConfigPreserveComments(configPath, initialCfg); errSave != nil {
		t.Fatalf("save initial config: %v", errSave)
	}
	watcherWrapper, errWatcher := defaultWatcherFactory(configPath, authDir, nil)
	if errWatcher != nil {
		t.Fatalf("create watcher: %v", errWatcher)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		cfg:         initialCfg,
		configPath:  configPath,
		coreManager: coreauth.NewManager(nil, nil, nil),
		watcher:     watcherWrapper,
	}
	service.ensureAuthUpdateQueue(ctx)
	watcherWrapper.SetAuthUpdateQueue(service.authUpdates)
	watcherWrapper.SetConfig(initialCfg)
	defer func() {
		watcherWrapper.SetAuthUpdateQueue(nil)
		cancel()
		if errStop := watcherWrapper.Stop(); errStop != nil {
			t.Errorf("stop watcher: %v", errStop)
		}
	}()

	baseURL := "https://shared.example.com/v1"
	apiKey := "codex-key"
	id, _ := synthesizer.NewStableIDGenerator().Next("codex:apikey", apiKey, baseURL)
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(id)
	})

	cfg := &config.Config{
		AuthDir: authDir,
		CodexKey: []config.CodexKey{{
			APIKey:  apiKey,
			BaseURL: baseURL,
			Models:  []internalconfig.CodexModel{{Name: "upstream-one", Alias: "codex-one"}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:     "codex",
			Disabled: true,
			BaseURL:  baseURL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "different-disabled-key",
			}},
			Models: []config.OpenAICompatibilityModel{{Name: "upstream-one", Alias: "codex-one"}},
		}},
	}
	if errSave := config.SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("save Codex config: %v", errSave)
	}
	service.syncCodexConfig()

	auth, ok := service.coreManager.GetByID(id)
	if !ok || auth == nil || auth.Disabled {
		t.Fatalf("Codex auth was not active when sync returned: auth=%+v", auth)
	}
	if auth.Index == "" {
		t.Fatal("Codex auth index was empty when sync returned")
	}
	modelIDs := func() map[string]bool {
		ids := make(map[string]bool)
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(id) {
			if model != nil {
				ids[model.ID] = true
			}
		}
		return ids
	}
	if ids := modelIDs(); !ids["codex-one"] {
		t.Fatalf("registered model IDs = %v, want codex-one", ids)
	}
	providers := registry.GetGlobalRegistry().GetModelProviders("codex-one")
	if len(providers) != 1 || providers[0] != "codex" {
		t.Fatalf("codex-one providers = %v, want [codex]", providers)
	}

	cfg.CodexKey[0].Models = []internalconfig.CodexModel{{Name: "upstream-two", Alias: "codex-two"}}
	if errSave := config.SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("save updated Codex config: %v", errSave)
	}
	service.syncCodexConfig()
	if ids := modelIDs(); !ids["codex-two"] || ids["codex-one"] {
		t.Fatalf("updated model IDs = %v, want codex-two without codex-one", ids)
	}

	cfg.CodexKey = nil
	if errSave := config.SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("save removed Codex config: %v", errSave)
	}
	service.syncCodexConfig()
	auth, ok = service.coreManager.GetByID(id)
	if !ok || auth == nil || !auth.Disabled {
		t.Fatalf("Codex auth was not disabled when removal sync returned: auth=%+v", auth)
	}
	if ids := modelIDs(); len(ids) != 0 {
		t.Fatalf("models remained after Codex removal: %v", ids)
	}
}

func TestForceHomeRuntimeConfigEnablesUsageStatistics(t *testing.T) {
	cfg := &config.Config{
		UsageStatisticsEnabled: false,
	}

	forceHomeRuntimeConfig(cfg)

	if !cfg.UsageStatisticsEnabled {
		t.Fatal("expected home runtime config to force usage statistics enabled")
	}
}

func TestApplyHomeOverlayForcesUsageStatisticsEnabled(t *testing.T) {
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	service := &Service{cfg: baseCfg}

	service.applyHomeOverlay(&config.Config{
		UsageStatisticsEnabled: false,
	})

	if service.cfg == nil || !service.cfg.UsageStatisticsEnabled {
		t.Fatal("expected home overlay to force usage statistics enabled")
	}
	if !service.cfg.Home.Enabled {
		t.Fatal("expected home overlay to preserve local home settings")
	}
}
