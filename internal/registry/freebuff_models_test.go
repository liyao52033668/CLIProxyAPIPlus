package registry

import (
	"strings"
	"testing"
)

// The catalog's first entry is the model a bare "freebuff"/"codebuff" request
// resolves to, so its position is load-bearing rather than cosmetic.
func TestFreebuffCatalogDefaultModelLeads(t *testing.T) {
	catalog := GetFreebuffCatalog()
	if len(catalog) == 0 {
		t.Fatal("freebuff catalog is empty")
	}
	const wantDefault = "z-ai/glm-5.3-flash"
	if catalog[0].ID != wantDefault {
		t.Fatalf("catalog[0].ID = %q, want %q", catalog[0].ID, wantDefault)
	}
	if !catalog[0].InPicker {
		t.Fatalf("default model %q must be offered in the upstream picker", wantDefault)
	}
}

// Every entry needs an agent id: upstream publishes no model list, so a model
// without one cannot be run at all.
func TestFreebuffCatalogEntriesHaveAgents(t *testing.T) {
	for _, entry := range GetFreebuffCatalog() {
		if entry.ID == "" {
			t.Fatal("freebuff catalog entry has an empty model id")
		}
		if entry.DisplayName == "" {
			t.Fatalf("freebuff catalog entry %q has no display name", entry.ID)
		}
		if entry.AgentID == "" {
			t.Fatalf("freebuff catalog entry %q has no agent id", entry.ID)
		}
		// The base3 harness is what the official CLI/Web run today; base2 is the
		// rollback path carried separately in LegacyAgentID.
		if !strings.HasPrefix(entry.AgentID, "base3-free") {
			t.Fatalf("freebuff catalog entry %q agent id %q is not a base3 root", entry.ID, entry.AgentID)
		}
		if entry.LegacyAgentID != "" && !strings.HasPrefix(entry.LegacyAgentID, "base2-free") {
			t.Fatalf("freebuff catalog entry %q legacy agent id %q is not a base2 root", entry.ID, entry.LegacyAgentID)
		}
		if entry.ContextLength <= 0 {
			t.Fatalf("freebuff catalog entry %q has no context window", entry.ID)
		}
	}
}

// Upstream keeps withdrawn models in its catalogs so released clients can be
// coerced onto a live row. We omit them instead: admission rejects them, so
// offering one only produces failures. The generator applies
// FREEBUFF_PAUSED_FREE_MODEL_IDS.
func TestFreebuffCatalogExcludesPausedModels(t *testing.T) {
	paused := map[string]string{
		"minimax/minimax-m3":              "MiniMax M3",
		"deepseek/deepseek-v4-pro":        "DeepSeek V4 Pro",
		"stealth/ox-alpha":                "Ox Alpha",
		"z-ai/glm-5.2":                    "GLM 5.2",
		"meta/muse-spark-1.3-contributor": "Muse Spark 1.3",
	}
	for _, entry := range GetFreebuffCatalog() {
		if name, ok := paused[entry.ID]; ok {
			t.Fatalf("freebuff catalog still offers withdrawn model %q (%s)", entry.ID, name)
		}
	}
}

// The model directory and the runtime fallback table must not drift apart.
func TestFreebuffModelsMatchCatalog(t *testing.T) {
	catalog := GetFreebuffCatalog()
	models := GetFreebuffModels()
	if len(models) != len(catalog) {
		t.Fatalf("GetFreebuffModels() = %d models, catalog = %d entries", len(models), len(catalog))
	}
	for i, entry := range catalog {
		if models[i].ID != entry.ID {
			t.Fatalf("models[%d].ID = %q, catalog[%d].ID = %q", i, models[i].ID, i, entry.ID)
		}
		if models[i].Type != "freebuff" || models[i].OwnedBy != "freebuff" {
			t.Fatalf("models[%d] = %+v, want freebuff ownership", i, models[i])
		}
	}
}
