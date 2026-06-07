package registry

import "testing"

func TestGetQoderModels_ThinkingCapabilities(t *testing.T) {
	models := GetQoderModels()
	index := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		index[model.ID] = model
	}

	tests := []struct {
		id     string
		levels []string
	}{
		{id: "qmodel_latest", levels: []string{"low", "medium", "high"}},
		{id: "qmodel", levels: []string{"low", "medium", "high"}},
		{id: "dmodel", levels: []string{"low", "medium", "high"}},
		{id: "dfmodel", levels: []string{"high", "max"}},
		{id: "gm51model", levels: []string{"low", "medium", "high"}},
		{id: "kmodel", levels: []string{"low", "medium", "high"}},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			model := index[tt.id]
			if model == nil {
				t.Fatalf("model %q not found", tt.id)
			}
			if model.Thinking == nil {
				t.Fatalf("model %q thinking = nil", tt.id)
			}
			if len(model.Thinking.Levels) != len(tt.levels) {
				t.Fatalf("model %q levels len = %d, want %d (%v)", tt.id, len(model.Thinking.Levels), len(tt.levels), tt.levels)
			}
			for i, level := range tt.levels {
				if model.Thinking.Levels[i] != level {
					t.Fatalf("model %q levels[%d] = %q, want %q", tt.id, i, model.Thinking.Levels[i], level)
				}
			}
		})
	}
}
