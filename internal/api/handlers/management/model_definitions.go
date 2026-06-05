package management

import (
	"net/http"
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

	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(strings.TrimSpace(channel)),
		"models":  models,
	})
}
