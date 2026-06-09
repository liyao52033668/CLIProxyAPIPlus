package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	codexsvc "github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
)

func (h *Handler) GetCodexInspectionSnapshot(c *gin.Context) {
	if h == nil || h.codexInspectionService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "codex inspection service unavailable"})
		return
	}

	snapshot, err := h.codexInspectionService.GetSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) RunCodexInspection(c *gin.Context) {
	if h == nil || h.codexInspectionService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "codex inspection service unavailable"})
		return
	}

	var req codexsvc.RunRequest
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
		}
	}
	req.TriggerType = codexsvc.TriggerTypeManual

	snapshot, err := h.codexInspectionService.Run(c.Request.Context(), req)
	if errors.Is(err, codexsvc.ErrRunAlreadyActive) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) UpdateCodexInspectionSettings(c *gin.Context) {
	if h == nil || h.codexInspectionService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "codex inspection service unavailable"})
		return
	}

	var settings codexsvc.InspectionSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	snapshot, err := h.codexInspectionService.UpdateSettings(c.Request.Context(), settings)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) ExecuteCodexInspectionActions(c *gin.Context) {
	if h == nil || h.codexInspectionService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "codex inspection service unavailable"})
		return
	}

	var req codexsvc.ExecuteActionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	result, err := h.codexInspectionService.ExecuteActions(c.Request.Context(), req)
	if errors.Is(err, codexsvc.ErrDeleteConfirmationRequired) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
