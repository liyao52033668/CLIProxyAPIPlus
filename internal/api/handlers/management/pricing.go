package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
)

type pricingEntryResponse struct {
	Model                string  `json:"model"`
	PromptPricePer1M     float64 `json:"prompt_price_per_1m"`
	CompletionPricePer1M float64 `json:"completion_price_per_1m"`
	CachePricePer1M      float64 `json:"cache_price_per_1m"`
}

type pricingListResponse struct {
	Pricing []pricingEntryResponse `json:"pricing"`
}

type updatePricingRequest struct {
	Model                string  `json:"model"`
	PromptPricePer1M     float64 `json:"prompt_price_per_1m"`
	CompletionPricePer1M float64 `json:"completion_price_per_1m"`
	CachePricePer1M      float64 `json:"cache_price_per_1m"`
}

func (h *Handler) ListPricing(c *gin.Context) {
	if h == nil || h.pricingService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pricing service not available"})
		return
	}

	settings, err := h.pricingService.ListPricing(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := make([]pricingEntryResponse, 0, len(settings))
	for _, setting := range settings {
		response = append(response, pricingEntryResponse{
			Model:                setting.Model,
			PromptPricePer1M:     setting.PromptPricePer1M,
			CompletionPricePer1M: setting.CompletionPricePer1M,
			CachePricePer1M:      setting.CachePricePer1M,
		})
	}
	c.JSON(http.StatusOK, pricingListResponse{Pricing: response})
}

func (h *Handler) UpdatePricing(c *gin.Context) {
	if h == nil || h.pricingService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pricing service not available"})
		return
	}

	var request updatePricingRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	setting, err := h.pricingService.UpdatePricing(c.Request.Context(), servicedto.UpdatePricingInput{
		Model:                model,
		PromptPricePer1M:     request.PromptPricePer1M,
		CompletionPricePer1M: request.CompletionPricePer1M,
		CachePricePer1M:      request.CachePricePer1M,
	})
	if err != nil {
		if strings.Contains(err.Error(), "has not been used") || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "non-negative") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, pricingEntryResponse{
		Model:                setting.Model,
		PromptPricePer1M:     setting.PromptPricePer1M,
		CompletionPricePer1M: setting.CompletionPricePer1M,
		CachePricePer1M:      setting.CachePricePer1M,
	})
}

func (h *Handler) DeletePricing(c *gin.Context) {
	if h == nil || h.pricingService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pricing service not available"})
		return
	}

	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	if err := h.pricingService.DeletePricing(c.Request.Context(), model); err != nil {
		if strings.Contains(err.Error(), "required") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}
