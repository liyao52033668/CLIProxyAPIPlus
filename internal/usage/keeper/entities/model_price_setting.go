package entities

import "time"

// ModelPriceSetting is a model price configuration entity used for per-model cost calculation.
type ModelPriceSetting struct {
	ID                   uint   `gorm:"primaryKey"`
	Model                string `gorm:"uniqueIndex:uniq_model_price_settings_model"`
	PromptPricePer1M     float64
	CompletionPricePer1M float64
	CachePricePer1M      float64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
