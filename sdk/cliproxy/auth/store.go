// Package auth provides authentication management, scheduling, and session handling for CLIProxyAPI.
package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// Save persists the provided auth record, replacing any existing one with same ID.
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}

// ProviderLister is an optional interface that Store implementations may
// satisfy to support filtered listing by provider. When a Store implements
// this interface, callers can avoid loading all records just to filter by
// provider in memory.
type ProviderLister interface {
	// ListByProvider returns only the auth records whose Provider matches
	// the given provider (case-insensitive comparison).
	ListByProvider(ctx context.Context, provider string) ([]*Auth, error)
}
