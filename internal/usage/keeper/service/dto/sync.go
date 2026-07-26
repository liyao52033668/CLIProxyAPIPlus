package dto

// SyncResult is the sync service result.
type SyncResult struct {
	Status         string
	InsertedEvents int
	DedupedEvents  int
}

// RedisBatchSyncResult is the result of a Redis batch sync.
type RedisBatchSyncResult struct {
	Empty          bool
	Status         string
	InsertedEvents int
	DedupedEvents  int
}

// RedisInboxPullResult is the Redis inbox pull result.
type RedisInboxPullResult struct {
	Empty        bool
	Status       string
	InsertedRows int
}

// ProviderMetadataInput is the service-layer input after provider metadata is flattened.
type ProviderMetadataInput struct {
	LookupKey    string
	Prefix       string
	ProviderType string
	DisplayName  string
	AuthIndex    string
}
