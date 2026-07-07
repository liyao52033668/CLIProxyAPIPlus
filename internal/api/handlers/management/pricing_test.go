package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	keeperservice "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
)

func newManagementPricingHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openManagementUsageTestDatabase(t)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey:    "pricing-event-1",
		Model:       "claude-sonnet",
		Timestamp:   time.Unix(1, 0),
		APIGroupKey: "provider-a",
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	h := &Handler{cfg: &config.Config{}}
	h.SetPricingService(keeperservice.NewPricingService(db))

	router := gin.New()
	router.GET("/pricing", h.ListPricing)
	router.PUT("/pricing", h.UpdatePricing)
	return h, router
}

func TestUpdatePricingStoresModelPriceSetting(t *testing.T) {
	_, router := newManagementPricingHandler(t)
	body := bytes.NewReader([]byte(`{"model":"claude-sonnet","prompt_price_per_1m":3,"completion_price_per_1m":15,"cache_price_per_1m":0.3}`))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/pricing", body)
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var payload struct {
		Pricing []struct {
			Model                string  `json:"model"`
			PromptPricePer1M     float64 `json:"prompt_price_per_1m"`
			CompletionPricePer1M float64 `json:"completion_price_per_1m"`
			CachePricePer1M      float64 `json:"cache_price_per_1m"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if len(payload.Pricing) != 1 {
		t.Fatalf("pricing len = %d, want 1", len(payload.Pricing))
	}
	setting := payload.Pricing[0]
	if setting.Model != "claude-sonnet" || setting.PromptPricePer1M != 3 || setting.CompletionPricePer1M != 15 || setting.CachePricePer1M != 0.3 {
		t.Fatalf("unexpected pricing setting: %+v", setting)
	}
}
