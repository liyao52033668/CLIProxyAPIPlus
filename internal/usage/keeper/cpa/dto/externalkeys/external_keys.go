package externalkeys

// ExternalAPIKeysResponse 是 CPA /management/external-api-keys 响应 DTO。
type ExternalAPIKeysResponse struct {
	ExternalAPIKeys []string `json:"api-keys"`
}
