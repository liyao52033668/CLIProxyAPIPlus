package registry

import (
	"testing"
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
