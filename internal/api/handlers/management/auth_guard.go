package management

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/access/guard"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// GetAuthGuardStatus returns the brute-force guard's current per-IP state
// together with the active policy.
func (h *Handler) GetAuthGuardStatus(c *gin.Context) {
	g := guard.Global()
	if g == nil {
		c.JSON(http.StatusOK, guard.Snapshot{Entries: []guard.Entry{}})
		return
	}
	c.JSON(http.StatusOK, g.Snapshot())
}

// EscalateIPToBlacklist permanently appends a client IP to the ip-blacklist
// section of the config file and refreshes the in-memory blacklist. It is
// invoked by the auth guard when a repeat offender crosses the configured
// escalation threshold.
func (h *Handler) EscalateIPToBlacklist(clientIP string) error {
	if h == nil || clientIP == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return fmt.Errorf("config not initialized")
	}
	for _, entry := range h.cfg.RemoteManagement.IPBlacklist {
		if entry == clientIP {
			return nil
		}
	}
	previous := h.cfg.RemoteManagement.IPBlacklist
	h.cfg.RemoteManagement.IPBlacklist = append(previous, clientIP)
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		h.cfg.RemoteManagement.IPBlacklist = previous
		return fmt.Errorf("failed to save config: %w", err)
	}
	guard.SetBlacklist(h.cfg.RemoteManagement.IPBlacklist)
	return nil
}
