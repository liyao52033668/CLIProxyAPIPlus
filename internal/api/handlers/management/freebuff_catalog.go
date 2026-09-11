package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// freebuffCatalogModel is one Freebuff model exposed to the management UI.
// It carries the root agent id because upstream publishes no model-list
// endpoint, so the UI cannot derive the agent from the model name.
type freebuffCatalogModel struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	AgentID       string                    `json:"agent_id"`
	LegacyAgentID string                    `json:"legacy_agent_id,omitempty"`
	InPicker      bool                      `json:"in_picker"`
	ContextLength int                       `json:"context_length,omitempty"`
	Thinking      *registry.ThinkingSupport `json:"thinking,omitempty"`
}

// GetFreebuffCatalog returns the compiled-in Freebuff model/agent catalog.
//
// Freebuff is an API-key provider whose upstream (codebuff.com) exposes no
// models endpoint, so unlike other API-key providers the management UI cannot
// proxy /v1/models. It reads this catalog instead, then lets the operator pick
// which entries to add. The catalog ships with the build; regenerate it with
// cmd/sync_freebuff_catalog when upstream changes.
func (h *Handler) GetFreebuffCatalog(c *gin.Context) {
	catalog := registry.GetFreebuffCatalog()
	models := make([]freebuffCatalogModel, 0, len(catalog))
	for _, entry := range catalog {
		if entry.ID == "" {
			continue
		}
		models = append(models, freebuffCatalogModel{
			ID:            entry.ID,
			Name:          entry.DisplayName,
			AgentID:       entry.AgentID,
			LegacyAgentID: entry.LegacyAgentID,
			InPicker:      entry.InPicker,
			ContextLength: entry.ContextLength,
			Thinking:      entry.Thinking,
		})
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}
