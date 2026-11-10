package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetStaticModelDefinitions returns model metadata for a given channel.
// It first checks the model registry for dynamically registered models,
// then falls back to static definitions if no models are registered.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	// Try to get models from registry first (includes dynamic models)
	reg := registry.GetGlobalRegistry()
	models := reg.GetAvailableModelsByProvider(channel)

	// Fallback to static definitions if registry has no models for this channel
	if len(models) == 0 {
		models = registry.GetStaticModelDefinitionsByChannel(channel)
	}

	if len(models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	models = preferUpstreamModelIDs(models)

	if len(models) > 1 {
		sort.Slice(models, func(i, j int) bool {
			if models[i] == nil {
				return models[j] != nil
			}
			if models[j] == nil {
				return false
			}
			return models[i].ID < models[j].ID
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(strings.TrimSpace(channel)),
		"models":  models,
	})
}

// preferUpstreamModelIDs restores the upstream model ID for entries that were
// rewritten by OAuth model aliasing at registration time. Aliased clones carry
// the real upstream ID in MetadataModelID (which is never serialized), so the
// management UI would otherwise only offer the already-aliased name when
// editing oauth-model-alias entries.
func preferUpstreamModelIDs(models []*registry.ModelInfo) []*registry.ModelInfo {
	needsRestore := false
	for _, model := range models {
		if model != nil && strings.TrimSpace(model.MetadataModelID) != "" {
			needsRestore = true
			break
		}
	}
	if !needsRestore {
		return models
	}

	out := make([]*registry.ModelInfo, 0, len(models))
	indexByID := make(map[string]int, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		clone := *model
		aliased := strings.TrimSpace(clone.MetadataModelID) != ""
		if aliased {
			clone.ID = strings.TrimSpace(clone.MetadataModelID)
		}
		key := strings.ToLower(strings.TrimSpace(clone.ID))
		if key == "" {
			continue
		}
		if idx, exists := indexByID[key]; exists {
			// Keep the pristine entry when both the original and its alias
			// clone resolve to the same upstream ID (fork=true aliases).
			if aliased {
				continue
			}
			out[idx] = &clone
			continue
		}
		indexByID[key] = len(out)
		out = append(out, &clone)
	}
	return out
}
