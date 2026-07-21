package registry

import (
	"testing"
	"time"
)

func TestGetFirstAvailableModelSkipsDisabledModels(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	})

	firstModel, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstModel != "gpt-4o" && firstModel != "gpt-4o-mini" {
		t.Errorf("expected gpt-4o or gpt-4o-mini, got %q", firstModel)
	}

	r.DisableAutoModel(firstModel, "client-1")

	secondModel, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error after disabling model: %v", err)
	}

	disabledModel := firstModel
	selectedModel := secondModel

	if selectedModel == disabledModel {
		t.Errorf("expected disabled model %q to be skipped, but got it again", disabledModel)
	}

	if selectedModel != "gpt-4o" && selectedModel != "gpt-4o-mini" {
		t.Errorf("expected the other available model, got %q", selectedModel)
	}
}

func TestGetFirstAvailableModelAllModelsDisabled(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
	})

	_, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r.DisableAutoModel("gpt-4o", "client-1")

	_, err = r.GetFirstAvailableModel("openai")
	if err == nil {
		t.Error("expected error when all models are disabled")
	}
}

func TestGetFirstAvailableModelWithMultipleClientsSameModel(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	})
	r.RegisterClient("client-2", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	})

	firstModel, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstModel != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", firstModel)
	}

	r.DisableAutoModel("gpt-4o", "client-1")

	secondModel, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error after disabling for client-1: %v", err)
	}

	if secondModel == "gpt-4o" {
		t.Errorf("expected gpt-4o to be skipped since it's disabled for client-1, but got %q", secondModel)
	}

	if secondModel != "gpt-4o-mini" {
		t.Errorf("expected gpt-4o-mini to be selected, got %q", secondModel)
	}
}

func TestGetFirstAvailableModelStickySessionDisabled(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
	})

	model, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", model)
	}

	r.DisableAutoModel("gpt-4o", "client-1")

	model, err = r.GetFirstAvailableModel("openai")
	if err == nil {
		t.Error("expected error when sticky model is disabled")
	}
}

func TestGetFirstAvailableModelRoundRobinSkipsDisabled(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
		{ID: "gpt-4-turbo", DisplayName: "GPT-4 Turbo"},
	})

	first := r.roundRobinIndex
	model1, _ := r.GetFirstAvailableModel("openai")
	second := r.roundRobinIndex

	if model1 != "gpt-4o" {
		t.Errorf("expected first model to be gpt-4o, got %q", model1)
	}
	if second == first {
		t.Error("round-robin index should have advanced")
	}

	r.DisableAutoModel("gpt-4o", "client-1")

	model2, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error after disabling gpt-4o: %v", err)
	}

	if model2 == "gpt-4o" {
		t.Errorf("expected disabled gpt-4o to be skipped, but got %q", model2)
	}
}

func TestIsModelDisabledForAnyAuth(t *testing.T) {
	r := newTestModelRegistry()

	if r.isModelDisabledForAnyAuthLocked("gpt-4o") {
		t.Error("expected model to not be disabled initially")
	}

	r.disableAutoModelLocked("gpt-4o", "client-1")

	if !r.isModelDisabledForAnyAuthLocked("gpt-4o") {
		t.Error("expected model to be disabled for client-1")
	}

	if r.isModelDisabledForAnyAuthLocked("gpt-4o-mini") {
		t.Error("expected gpt-4o-mini to not be disabled")
	}

	r.disableAutoModelLocked("gpt-4o", "client-2")

	if !r.isModelDisabledForAnyAuthLocked("gpt-4o") {
		t.Error("expected model to still be disabled")
	}
}

func TestGetAvailableModelsShowsAllModelsIncludingDisabled(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	})

	r.DisableAutoModel("gpt-4o", "client-1")

	available := r.GetAvailableModels("openai")
	found := false
	for _, model := range available {
		if id, ok := model["id"].(string); ok && id == "gpt-4o" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected gpt-4o to appear in GetAvailableModels (API lists all models)")
	}
}

func TestGetFirstAvailableModelPrefersHighestPriorityModelSet(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-low", "openai", []*ModelInfo{{ID: "gpt-4o", DisplayName: "GPT-4o"}})
	r.RegisterClient("client-high", "openai", []*ModelInfo{{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"}})
	r.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4o":
			return 10, true
		case "gpt-4o-mini":
			return 30, true
		default:
			return 0, false
		}
	})

	model, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Fatalf("expected highest-priority model gpt-4o-mini, got %q", model)
	}
}

// Regression: highestPriorityFunc may re-enter the registry (e.g. ClientSupportsModel).
// Holding r.mutex.Lock across that callback deadlocked global auto model resolution
// and froze the whole service (mutex not cancellable by request context).
func TestGetFirstAvailableModelDoesNotDeadlockWhenPriorityFuncReentersRegistry(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	})
	r.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		if !r.ClientSupportsModel("client-1", modelID) {
			return 0, false
		}
		if modelID == "gpt-4o-mini" {
			return 30, true
		}
		return 10, true
	})

	done := make(chan struct{})
	var model string
	var err error
	go func() {
		defer close(done)
		model, err = r.GetFirstAvailableModel("openai")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GetFirstAvailableModel deadlocked when highestPriorityFunc re-entered registry")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Fatalf("expected gpt-4o-mini, got %q", model)
	}
}

func TestGetFirstAvailableModelGlobalAutoDoesNotDeadlockWhenPriorityFuncReentersRegistry(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{
		{ID: "gpt-4o", DisplayName: "GPT-4o", Created: 100},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini", Created: 200},
	})
	r.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		// Simulate production HighestAvailablePriorityForModel path for global auto (handlerType "").
		if !r.ClientSupportsModel("client-1", modelID) {
			return 0, false
		}
		return 10, true
	})

	done := make(chan struct{})
	var model string
	var err error
	go func() {
		defer close(done)
		model, err = r.GetFirstAvailableModelExcluding("", map[string]struct{}{"auto": {}})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("global auto GetFirstAvailableModelExcluding deadlocked when highestPriorityFunc re-entered registry")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without usage history, global auto falls back to smaller modelID.
	if model != "gpt-4o" {
		t.Fatalf("expected gpt-4o (smaller modelID when never selected), got %q", model)
	}
}

func TestGetFirstAvailableModelGlobalAutoTieBreakOrder(t *testing.T) {
	// recently selected wins
	t.Run("prefers_recently_selected", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("c1", "openai", []*ModelInfo{
			{ID: "model-a", Created: 100},
			{ID: "model-b", Created: 200},
		})
		// Mark model-a as more recently selected even though model-b has newer created.
		r.mutex.Lock()
		r.models["model-a"].LastSelectedAt = time.Now()
		r.models["model-b"].LastSelectedAt = time.Now().Add(-time.Hour)
		r.mutex.Unlock()

		model, err := r.GetFirstAvailableModelExcluding("", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "model-a" {
			t.Fatalf("expected model-a (more recently selected), got %q", model)
		}
	})

	// never selected -> more auth clients wins (created ignored)
	t.Run("prefers_more_auth_clients", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("c1", "openai", []*ModelInfo{
			{ID: "model-a", Created: 300},
			{ID: "model-b", Created: 100},
		})
		r.RegisterClient("c2", "openai", []*ModelInfo{
			{ID: "model-b", Created: 100},
		})

		model, err := r.GetFirstAvailableModelExcluding("", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "model-b" {
			t.Fatalf("expected model-b (more auth clients), got %q", model)
		}
	})

	// never selected + same auth count -> smaller modelID (created ignored)
	t.Run("prefers_smaller_model_id", func(t *testing.T) {
		r := newTestModelRegistry()
		r.RegisterClient("c1", "openai", []*ModelInfo{
			{ID: "model-z", Created: 300},
			{ID: "model-a", Created: 100},
		})

		model, err := r.GetFirstAvailableModelExcluding("", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if model != "model-a" {
			t.Fatalf("expected model-a (smaller modelID), got %q", model)
		}
	})
}

func TestGetFirstAvailableModelRoundRobinWithinPriorityTie(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "gpt-4o", DisplayName: "GPT-4o"}})
	r.RegisterClient("client-2", "openai", []*ModelInfo{{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"}})
	r.RegisterClient("client-3", "openai", []*ModelInfo{{ID: "gpt-4-turbo", DisplayName: "GPT-4 Turbo"}})
	r.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4o", "gpt-4o-mini":
			return 30, true
		case "gpt-4-turbo":
			return 10, true
		default:
			return 0, false
		}
	})

	model1, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model1 != "gpt-4o" {
		t.Fatalf("expected first tied top-priority model gpt-4o, got %q", model1)
	}

	r.RecordModelResult("openai", model1, "client-1", false)

	model2, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error after failover: %v", err)
	}
	if model2 != "gpt-4o-mini" {
		t.Fatalf("expected failover within tied top-priority set to gpt-4o-mini, got %q", model2)
	}
}

func TestGetFirstAvailableModelStickyInvalidatesWhenTopPriorityChanges(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "gpt-4o", DisplayName: "GPT-4o"}})
	r.RegisterClient("client-2", "openai", []*ModelInfo{{ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"}})
	priorityByModel := map[string]int{
		"gpt-4o":      30,
		"gpt-4o-mini": 20,
	}
	r.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		priority, ok := priorityByModel[modelID]
		return priority, ok
	})

	model, err := r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gpt-4o" {
		t.Fatalf("expected initial top-priority model gpt-4o, got %q", model)
	}

	priorityByModel["gpt-4o"] = 10
	priorityByModel["gpt-4o-mini"] = 40

	model, err = r.GetFirstAvailableModel("openai")
	if err != nil {
		t.Fatalf("unexpected error after priority change: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Fatalf("expected sticky invalidation to switch to new top-priority model gpt-4o-mini, got %q", model)
	}
}
