package auth

import (
	"context"
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ForceRefreshResult records the outcome of a forced refresh for one credential.
type ForceRefreshResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// refreshWorkers resolves the refresh worker pool size from the runtime config,
// falling back to the default concurrency when unset or invalid.
func (m *Manager) refreshWorkers() int {
	workers := refreshMaxConcurrency
	if m != nil {
		if cfg, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && cfg != nil && cfg.AuthAutoRefreshWorkers > 0 {
			workers = cfg.AuthAutoRefreshWorkers
		}
	}
	return workers
}

// authHasRefreshCredential reports whether the auth carries a refresh token in
// its metadata or relies on a runtime refresh evaluator.
func authHasRefreshCredential(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if authRefreshToken(auth) != "" {
		return true
	}
	return auth.Runtime != nil
}

// ForceRefreshAll triggers an immediate refresh for all credentials that have
// refresh credentials or runtime refresh evaluators. Concurrency is bounded by
// the refresh worker pool so a large credential set cannot exhaust upstream or
// local resources; queued jobs fast-fail when the context is canceled.
func (m *Manager) ForceRefreshAll(ctx context.Context) []ForceRefreshResult {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.auths))
	for id, auth := range m.auths {
		if auth != nil && !auth.Disabled && auth.Status != StatusDisabled && authHasRefreshCredential(auth) {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	results := make([]ForceRefreshResult, len(ids))
	if len(ids) == 0 {
		return results
	}

	workers := m.refreshWorkers()
	if workers <= 0 {
		workers = 1
	}
	if workers > len(ids) {
		workers = len(ids)
	}

	type refreshJob struct {
		index  int
		authID string
	}

	jobCh := make(chan refreshJob, len(ids))
	for i, id := range ids {
		jobCh <- refreshJob{index: i, authID: id}
	}
	close(jobCh)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if errCtx := ctx.Err(); errCtx != nil {
					results[job.index] = ForceRefreshResult{
						ID:      job.authID,
						Success: false,
						Error:   errCtx.Error(),
					}
					continue
				}

				if _, err := m.refreshAuth(ctx, job.authID); err != nil {
					results[job.index] = ForceRefreshResult{ID: job.authID, Success: false, Error: err.Error()}
				} else {
					results[job.index] = ForceRefreshResult{ID: job.authID, Success: true}
				}
			}
		}()
	}
	wg.Wait()
	return results
}
