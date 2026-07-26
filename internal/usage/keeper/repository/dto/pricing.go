package dto

// ModelPriceSettingInput is the write payload for price settings.
type ModelPriceSettingInput struct {
	Model                string
	PromptPricePer1M     float64
	CompletionPricePer1M float64
	CachePricePer1M      float64
}
