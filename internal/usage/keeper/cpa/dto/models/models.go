package models

// ModelsResponse 是 CPA OpenAI-compatible /models 响应 DTO。
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ModelInfo 是 CPA OpenAI-compatible /models 响应中的单个模型 DTO。
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}
