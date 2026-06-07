package executor

import (
	"strings"
	"sync"
)

type QoderModelContractCache struct {
	mu        sync.RWMutex
	contracts map[string]map[string]QoderModelContract
}

func newQoderModelContractCache() *QoderModelContractCache {
	return &QoderModelContractCache{contracts: make(map[string]map[string]QoderModelContract)}
}

func StoreQoderModelContracts(authID string, contracts map[string]QoderModelContract) {
	sharedQoderModelContractCache.store(authID, contracts)
}

func LoadQoderModelContract(authID, modelKey string) (QoderModelContract, bool) {
	return sharedQoderModelContractCache.load(authID, modelKey)
}

func ClearQoderModelContracts(authID string) {
	sharedQoderModelContractCache.clear(authID)
}

func (c *QoderModelContractCache) load(authID, modelKey string) (QoderModelContract, bool) {
	normalizedAuthID := normalizeQoderModelContractCacheKey(authID)
	normalizedModelKey := normalizeQoderModelContractCacheKey(modelKey)
	if normalizedAuthID == "" || normalizedModelKey == "" {
		return QoderModelContract{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	authContracts, ok := c.contracts[normalizedAuthID]
	if !ok {
		return QoderModelContract{}, false
	}
	contract, ok := authContracts[normalizedModelKey]
	return contract, ok
}

func (c *QoderModelContractCache) store(authID string, contracts map[string]QoderModelContract) {
	normalizedAuthID := normalizeQoderModelContractCacheKey(authID)
	if normalizedAuthID == "" {
		return
	}
	copiedContracts := make(map[string]QoderModelContract)
	for modelKey, contract := range contracts {
		normalizedModelKey := normalizeQoderModelContractCacheKey(modelKey)
		if normalizedModelKey == "" {
			continue
		}
		copiedContracts[normalizedModelKey] = contract
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(copiedContracts) == 0 {
		delete(c.contracts, normalizedAuthID)
		return
	}
	c.contracts[normalizedAuthID] = copiedContracts
}

func (c *QoderModelContractCache) clear(authID string) {
	normalizedAuthID := normalizeQoderModelContractCacheKey(authID)
	if normalizedAuthID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.contracts, normalizedAuthID)
}

func normalizeQoderModelContractCacheKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
