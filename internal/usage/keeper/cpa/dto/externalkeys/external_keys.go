package externalkeys

// ExternalAPIKeysResponse is the CPA /management/external-api-keys response DTO.
type ExternalAPIKeysResponse struct {
	ExternalAPIKeys []string `json:"api-keys"`
}
