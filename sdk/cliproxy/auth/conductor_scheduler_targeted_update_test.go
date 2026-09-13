package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_MarkResult_TargetedModelShardUpdate(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	authID := "auth-targeted-test"
	provider := "custom-prov"
	models := []*registry.ModelInfo{
		{ID: "model-a"},
		{ID: "model-b"},
		{ID: "model-c"},
	}
	reg.RegisterClient(authID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// Warm up scheduler model shards for all 3 models.
	for _, m := range []string{"model-a", "model-b", "model-c"} {
		picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, m, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || picked.ID != authID {
			t.Fatalf("pickSingle(%s) = (%v, %v), want %s", m, picked, errPick, authID)
		}
	}

	// Capture entries and meta pointers before single-model MarkResult.
	manager.scheduler.mu.Lock()
	pState := manager.scheduler.providers[provider]
	if pState == nil {
		manager.scheduler.mu.Unlock()
		t.Fatalf("provider state for %s is nil", provider)
	}
	shardA := pState.modelShards["model-a"]
	shardB := pState.modelShards["model-b"]
	shardC := pState.modelShards["model-c"]
	if shardA == nil || shardB == nil || shardC == nil {
		manager.scheduler.mu.Unlock()
		t.Fatalf("expected shards for model-a, model-b, and model-c to exist")
	}
	entryBBefore := shardB.entries[authID].meta
	entryCBefore := shardC.entries[authID].meta
	manager.scheduler.mu.Unlock()

	// Trigger single-model 500 error on model-a.
	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    "model-a",
		Success:  false,
		Error: &Error{
			Code:       "internal_server_error",
			Message:    "500 Internal Server Error",
			HTTPStatus: http.StatusInternalServerError,
		},
	})

	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()

	// 1. Verify model-a shard was updated into blocked / cooldown state.
	entryA := shardA.entries[authID]
	if entryA == nil || (entryA.state != scheduledStateBlocked && entryA.state != scheduledStateCooldown) {
		t.Fatalf("model-a shard state = %v, want scheduledStateBlocked or scheduledStateCooldown", entryA.state)
	}

	// 2. Verify model-b and model-c shards were NOT visited/rebuilt.
	entryBAfter := shardB.entries[authID].meta
	entryCAfter := shardC.entries[authID].meta
	if entryBAfter != entryBBefore {
		t.Fatalf("unrelated shard model-b was touched/updated by model-a MarkResult (meta pointer changed from %p to %p)", entryBBefore, entryBAfter)
	}
	if entryCAfter != entryCBefore {
		t.Fatalf("unrelated shard model-c was touched/updated by model-a MarkResult (meta pointer changed from %p to %p)", entryCBefore, entryCAfter)
	}

	// 3. Verify model-b and model-c remain in ready state.
	if shardB.entries[authID].state != scheduledStateReady {
		t.Fatalf("model-b state = %v, want scheduledStateReady", shardB.entries[authID].state)
	}
	if shardC.entries[authID].state != scheduledStateReady {
		t.Fatalf("model-c state = %v, want scheduledStateReady", shardC.entries[authID].state)
	}
}

func TestManager_MarkResult_SuccessTargetedUpdate(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	authID := "auth-success-targeted-test"
	provider := "custom-prov-succ"
	models := []*registry.ModelInfo{
		{ID: "model-1"},
		{ID: "model-2"},
	}
	reg.RegisterClient(authID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	for _, m := range []string{"model-1", "model-2"} {
		picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, m, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || picked.ID != authID {
			t.Fatalf("pickSingle(%s) error = %v", m, errPick)
		}
	}

	manager.scheduler.mu.Lock()
	pState := manager.scheduler.providers[provider]
	shard2 := pState.modelShards["model-2"]
	entry2Before := shard2.entries[authID].meta
	manager.scheduler.mu.Unlock()

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    "model-1",
		Success:  true,
	})

	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()

	entry2After := shard2.entries[authID].meta
	if entry2After != entry2Before {
		t.Fatalf("unrelated shard model-2 was touched/updated by model-1 success MarkResult (meta pointer changed from %p to %p)", entry2Before, entry2After)
	}
}

func TestScheduler_MarkResult_OutOfOrderCrossModelUpdates(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	authID := "auth-ooo-test"
	provider := "custom-prov-ooo"
	models := []*registry.ModelInfo{
		{ID: "model-p"},
		{ID: "model-q"},
	}
	reg.RegisterClient(authID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	for _, m := range []string{"model-p", "model-q"} {
		picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, m, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || picked.ID != authID {
			t.Fatalf("pickSingle(%s) error = %v", m, errPick)
		}
	}

	now := time.Now()
	// Simulate Request 1 (model-p failure): older snapshot
	authSnapshot1 := auth.Clone()
	authSnapshot1.UpdatedAt = now
	authSnapshot1.ModelStates = map[string]*ModelState{
		"model-p": {
			Unavailable:    true,
			Status:         StatusError,
			NextRetryAfter: now.Add(10 * time.Minute),
		},
	}

	// Simulate Request 2 (model-q failure): newer snapshot (contains both model-p and model-q failures)
	authSnapshot2 := auth.Clone()
	authSnapshot2.UpdatedAt = now.Add(time.Millisecond)
	authSnapshot2.ModelStates = map[string]*ModelState{
		"model-p": {
			Unavailable:    true,
			Status:         StatusError,
			NextRetryAfter: now.Add(10 * time.Minute),
		},
		"model-q": {
			Unavailable:    true,
			Status:         StatusError,
			NextRetryAfter: now.Add(10 * time.Minute),
		},
	}

	// Out-of-order execution: Request 2 arrives at scheduler FIRST with target "model-q"
	manager.scheduler.upsertAuthResult(authSnapshot2, []string{"model-q"}, false)

	// Request 1 arrives SECOND with the older snapshot and target "model-p"
	manager.scheduler.upsertAuthResult(authSnapshot1, []string{"model-p"}, false)

	// Both shards MUST reflect cooldown / blocked state despite the out-of-order arrival
	manager.scheduler.mu.Lock()
	pState := manager.scheduler.providers[provider]
	shardP := pState.modelShards["model-p"]
	shardQ := pState.modelShards["model-q"]
	manager.scheduler.mu.Unlock()

	if shardQ.entries[authID].state == scheduledStateReady {
		t.Fatalf("model-q shard state should not be ready")
	}
	if shardP.entries[authID].state == scheduledStateReady {
		t.Fatalf("model-p shard state should not be ready even when the older snapshot arrived second")
	}

	// Verify pickSingle cannot pick auth-ooo-test for either model
	if _, errPickP := manager.scheduler.pickSingle(context.Background(), provider, "model-p", cliproxyexecutor.Options{}, nil); errPickP == nil {
		t.Fatalf("pickSingle(model-p) should fail due to cooldown")
	}
	if _, errPickQ := manager.scheduler.pickSingle(context.Background(), provider, "model-q", cliproxyexecutor.Options{}, nil); errPickQ == nil {
		t.Fatalf("pickSingle(model-q) should fail due to cooldown")
	}
}
