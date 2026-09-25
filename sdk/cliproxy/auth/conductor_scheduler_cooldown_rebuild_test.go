package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestScheduler_ModelCooldown_DoesNotTriggerRebuild verifies that when credentials
// for a model enter cooldown, pick failures do NOT trigger full scheduler rebuilds.
// Rebuilding on cooldown pick failure causes catastrophic lock contention (issue #5988).
func TestScheduler_ModelCooldown_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "scheduler-cooldown-no-rebuild-model"
	auth := &Auth{
		ID:       "auth-cooldown-no-rebuild",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Verify initial pick succeeds.
	picked, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("initial pickSingle failed: picked=%v, err=%v", picked, errPick)
	}

	// Put credential into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Directly verify shouldRetrySchedulerPick does NOT treat modelCooldownError as retryable.
	_, errCooldown := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errCooldown, &targetCooldownErr) {
		t.Fatalf("expected pickSingle to return *modelCooldownError, got: %v", errCooldown)
	}
	if shouldRetrySchedulerPick(errCooldown) {
		t.Fatalf("shouldRetrySchedulerPick returned true for modelCooldownError; cooldown must not trigger scheduler rebuild")
	}

	// Capture scheduler provider pointer before pickNext.
	manager.scheduler.mu.Lock()
	providerSchedulerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Calling pickNext on a cooled down model should return modelCooldownError without rebuilding.
	_, _, errPickNext := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickNext == nil {
		t.Fatal("expected pickNext to fail with cooldown error, got nil")
	}
	if !errors.As(errPickNext, &targetCooldownErr) {
		t.Fatalf("expected pickNext error to be *modelCooldownError, got: %v", errPickNext)
	}

	manager.scheduler.mu.Lock()
	providerSchedulerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// In the buggy implementation, syncScheduler re-allocates providers map and providerScheduler during rebuild.
	if providerSchedulerBefore != providerSchedulerAfter {
		t.Fatalf("pickNext unexpectedly triggered a full scheduler rebuild on cooldown error")
	}

	// Verify that syncedVersion has caught up to currentVersion so subsequent picks take the fast-path.
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not advance to currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// Subsequent pickNext call must take the fast path without scanning.
	_, _, errSubsequent := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if !errors.As(errSubsequent, &targetCooldownErr) {
		t.Fatalf("expected subsequent pickNext error to be *modelCooldownError, got: %v", errSubsequent)
	}
}

// TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel verifies that concurrent
// requests for a cooling-down model do not trigger rebuilds or starve picks on healthy models.
func TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	cooledModel := "model-cooling"
	healthyModel := "model-healthy"

	authCool := &Auth{ID: "auth-cool", Provider: "gemini"}
	authHealthy := &Auth{ID: "auth-healthy", Provider: "gemini"}

	if _, errRegisterCool := manager.Register(ctx, authCool); errRegisterCool != nil {
		t.Fatalf("register authCool: %v", errRegisterCool)
	}
	if _, errRegisterHealthy := manager.Register(ctx, authHealthy); errRegisterHealthy != nil {
		t.Fatalf("register authHealthy: %v", errRegisterHealthy)
	}

	registerSchedulerModels(t, "gemini", cooledModel, authCool.ID)
	registerSchedulerModels(t, "gemini", healthyModel, authHealthy.ID)
	manager.RefreshSchedulerEntry(authCool.ID)
	manager.RefreshSchedulerEntry(authHealthy.ID)

	// Put authCool into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   authCool.ID,
		Provider: "gemini",
		Model:    cooledModel,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Launch concurrent picks for cooledModel.
	var wg sync.WaitGroup
	const concurrency = 30
	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, _, _ = manager.pickNext(ctx, "gemini", cooledModel, cliproxyexecutor.Options{}, nil)
		}()
	}

	// Healthy model pick must complete quickly without being blocked behind rebuild storms.
	healthyDone := make(chan struct{})
	go func() {
		<-startGate
		picked, exec, errPick := manager.pickNext(ctx, "gemini", healthyModel, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || exec == nil {
			t.Errorf("healthy pick failed: picked=%v, err=%v", picked, errPick)
		}
		close(healthyDone)
	}()

	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	// Start all concurrent goroutines simultaneously.
	close(startGate)

	// Verify healthy model completes while cooldown picks are underway.
	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy model pick was blocked/starved by concurrent cooldown picks")
	}

	// Verify all cooldown workers finish within timeout.
	select {
	case <-workersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cooldown pick workers did not complete within timeout")
	}

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("concurrent cooldown picks triggered scheduler rebuild(s)")
	}
}

// TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild verifies that ongoing,
// concurrent MarkResult operations (which update model states) do NOT trigger full
// scheduler rebuilds when pick operations fail for cooled models.
func TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	modelActive := "model-active"
	modelCooldown := "model-in-cooldown"

	authActive := &Auth{ID: "auth-active", Provider: "gemini"}
	authCooled := &Auth{ID: "auth-cooled", Provider: "gemini"}

	if _, errRegisterActive := manager.Register(ctx, authActive); errRegisterActive != nil {
		t.Fatalf("register authActive: %v", errRegisterActive)
	}
	if _, errRegisterCooled := manager.Register(ctx, authCooled); errRegisterCooled != nil {
		t.Fatalf("register authCooled: %v", errRegisterCooled)
	}

	registerSchedulerModels(t, "gemini", modelActive, authActive.ID)
	registerSchedulerModels(t, "gemini", modelCooldown, authCooled.ID)
	manager.RefreshSchedulerEntry(authActive.ID)
	manager.RefreshSchedulerEntry(authCooled.ID)

	// Put authCooled into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   authCooled.ID,
		Provider: "gemini",
		Model:    modelCooldown,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	var stopFlag atomic.Bool
	var wg sync.WaitGroup

	// Background worker continuously calling MarkResult on authActive to mutate scheduler state.
	const resultWorkers = 5
	for i := 0; i < resultWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				manager.MarkResult(ctx, Result{
					AuthID:   authActive.ID,
					Provider: "gemini",
					Model:    modelActive,
					Success:  true,
				})
			}
		}()
	}

	// Concurrent workers attempting pickNext on modelCooldown while MarkResult is actively mutating state.
	const pickWorkers = 20
	pickDone := make(chan struct{})
	go func() {
		var pickWg sync.WaitGroup
		for i := 0; i < pickWorkers; i++ {
			pickWg.Add(1)
			go func() {
				defer pickWg.Done()
				for j := 0; j < 10; j++ {
					_, _, errPick := manager.pickNext(ctx, "gemini", modelCooldown, cliproxyexecutor.Options{}, nil)
					if errPick == nil {
						t.Errorf("expected pickNext on cooldown model to fail")
					}
				}
			}()
		}
		pickWg.Wait()
		close(pickDone)
	}()

	select {
	case <-pickDone:
	case <-time.After(2 * time.Second):
		stopFlag.Store(true)
		t.Fatal("pick workers timed out while MarkResult was running")
	}

	stopFlag.Store(true)
	wg.Wait()

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("interleaved MarkResult caused unexpected scheduler rebuild(s)")
	}
}

// TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState verifies that a
// rebuild executing with a slightly stale snapshot must not downgrade scheduler state
// that advanced via an in-flight MarkResult (cooldown applied or recovered).
func TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-concurrent-mark-rebuild"
	auth := &Auth{
		ID:       "auth-concurrent-mark",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Case A: snapshot taken while healthy, concurrent MarkResult puts auth into cooldown.
	manager.mu.RLock()
	healthySnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Rebuild with the older healthy snapshot.
	manager.scheduler.rebuild([]*Auth{healthySnapshot})

	// Cooldown state must be preserved (must NOT revert to healthy).
	_, errPickCooldown := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errPickCooldown, &targetCooldownErr) {
		t.Fatalf("rebuild with stale snapshot reverted cooldown state; expected *modelCooldownError, got: %v", errPickCooldown)
	}

	// Case B: snapshot taken during cooldown, concurrent MarkResult recovers the auth.
	manager.mu.RLock()
	cooledSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// Clear cooldown via a successful result (local equivalent of upstream ResetQuota).
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  true,
	})

	// Rebuild with older cooled snapshot.
	manager.scheduler.rebuild([]*Auth{cooledSnapshot})

	// Recovered state must be preserved (must NOT revert to cooldown).
	pickedRecovered, errPickRecovered := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickRecovered != nil || pickedRecovered == nil || pickedRecovered.ID != auth.ID {
		t.Fatalf("rebuild with stale snapshot reverted recovered state: picked=%v, err=%v", pickedRecovered, errPickRecovered)
	}
}

// TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath verifies that quota
// cooldowns applied via MarkResult do not change the structural version, ensuring the
// fast path remains active during cooldowns.
func TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-projection-fastpath"
	auth := &Auth{
		ID:       "auth-proj-fastpath",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	manager.syncScheduler()
	initialVersion := manager.currentVersion()
	if initialVersion != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), initialVersion)
	}

	// Apply cooldown via MarkResult.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Structural version must NOT have changed.
	if manager.currentVersion() != initialVersion {
		t.Fatalf("currentVersion changed from %d to %d on quota cooldown; cooldown must not invalidate structural registration epoch", initialVersion, manager.currentVersion())
	}

	// Fast path is confirmed intact.
	_, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if manager.shouldRetrySchedulerPick(errPick, initialVersion) {
		t.Fatal("shouldRetrySchedulerPick unexpectedly returned true on cooldown error")
	}
}

// TestScheduler_RegisterClient_InvalidatesFastPath verifies that external model registry
// changes (RegisterClient) reliably invalidate the fast-path even if a previous sync
// already occurred.
func TestScheduler_RegisterClient_InvalidatesFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	initialModel := "model-init"
	dynamicModel := "model-dynamic"

	auth := &Auth{
		ID:       "auth-dynamic-reg",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", initialModel, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// Ensure manager and scheduler are completely synced (syncedVersion == currentVersion).
	manager.syncScheduler()
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// Register dynamicModel directly into global registry without calling RefreshSchedulerEntry.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{
		{ID: initialModel},
		{ID: dynamicModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// currentVersion must have increased because the registry epoch changed.
	if manager.currentVersion() <= manager.syncedVersion.Load() {
		t.Fatalf("currentVersion (%d) did not increase after RegisterClient (syncedVersion=%d)", manager.currentVersion(), manager.syncedVersion.Load())
	}

	// pickNext on dynamicModel must succeed by discovering the new model.
	picked, exec, errPick := manager.pickNext(ctx, "gemini", dynamicModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext failed to discover dynamically registered model: %v", errPick)
	}
	if picked == nil || picked.ID != auth.ID || exec == nil {
		t.Fatalf("pickNext returned unexpected auth=%v, exec=%v", picked, exec)
	}
}

// TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop verifies that unschedulable
// auths (e.g. auths with empty provider) are not counted as schedulable auths, preventing
// an infinite rebuild loop where activeCount never matches s.authProviders.
func TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	model := "model-unschedulable-test"

	// Register a valid auth that will cool down.
	validAuth := &Auth{
		ID:       "auth-valid-cooling",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, validAuth); errRegister != nil {
		t.Fatalf("register valid auth: %v", errRegister)
	}
	registerSchedulerModels(t, "gemini", model, validAuth.ID)
	manager.RefreshSchedulerEntry(validAuth.ID)

	// Register an unschedulable auth directly in manager (empty provider).
	unschedulableAuth := &Auth{
		ID:       "auth-empty-provider",
		Provider: "", // Unschedulable!
	}
	if _, errRegister := manager.Register(ctx, unschedulableAuth); errRegister != nil {
		t.Fatalf("register unschedulable auth: %v", errRegister)
	}

	// Put validAuth into cooldown.
	manager.MarkResult(ctx, Result{
		AuthID:   validAuth.ID,
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Initial pick on model should sync once and converge.
	_, _, errPickInitial := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPickInitial == nil {
		t.Fatal("expected pickNext to fail with cooldown error")
	}

	// Synced version must have converged to currentVersion despite the unschedulable auth.
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not converge to currentVersion (%d) in presence of unschedulable auth",
			manager.syncedVersion.Load(), manager.currentVersion())
	}

	manager.scheduler.mu.Lock()
	providersBefore := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	// Subsequent pick attempts on the cooling model MUST NOT trigger rebuilds.
	for i := 0; i < 5; i++ {
		_, _, _ = manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	}

	manager.scheduler.mu.Lock()
	providersAfter := manager.scheduler.providers["gemini"]
	manager.scheduler.mu.Unlock()

	if providersBefore != providersAfter {
		t.Fatalf("subsequent picks triggered scheduler rebuild(s) due to unschedulable auth mismatch")
	}
}
