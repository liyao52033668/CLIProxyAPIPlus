package auth

import (
	"context"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// InFlightSnapshot is an immutable view of standalone in-flight executions.
type InFlightSnapshot struct {
	InFlight        int64
	OldestStartedAt time.Time
	ByProvider      map[string]int64
}

// inFlightTracker counts in-flight local executions for standalone instances.
// It stays disabled while Home mode is active because the execution registry
// already tracks Home-dispatched executions for that lifetime.
type inFlightTracker struct {
	mu             sync.Mutex
	count          int64
	oldestStarted  time.Time
	byProvider     map[string]int64
	inflightScopes map[*inFlightScope]time.Time
}

func newInFlightTracker() *inFlightTracker {
	return &inFlightTracker{
		byProvider:     make(map[string]int64),
		inflightScopes: make(map[*inFlightScope]time.Time),
	}
}

// inFlightScope represents one tracked execution until its end is called.
type inFlightScope struct {
	tracker  *inFlightTracker
	provider string
	done     sync.Once
}

// setProvider updates the provider attribution of an in-flight scope without
// changing the total count or its start time.
func (s *inFlightScope) setProvider(provider string) {
	if s == nil || s.tracker == nil {
		return
	}
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.inflightScopes[s]; !ok || s.provider == provider {
		return
	}
	if next := t.byProvider[s.provider] - 1; next > 0 {
		t.byProvider[s.provider] = next
	} else {
		delete(t.byProvider, s.provider)
	}
	s.provider = provider
	t.byProvider[provider]++
}

// end releases the scope exactly once.
func (s *inFlightScope) end() {
	if s == nil || s.tracker == nil {
		return
	}
	s.done.Do(func() {
		t := s.tracker
		t.mu.Lock()
		defer t.mu.Unlock()
		startedAt, ok := t.inflightScopes[s]
		if !ok {
			return
		}
		delete(t.inflightScopes, s)
		t.count--
		if next := t.byProvider[s.provider] - 1; next > 0 {
			t.byProvider[s.provider] = next
		} else {
			delete(t.byProvider, s.provider)
		}
		if t.count == 0 {
			t.oldestStarted = time.Time{}
		} else if startedAt.Equal(t.oldestStarted) {
			t.recomputeOldestLocked()
		}
	})
}

func (t *inFlightTracker) recomputeOldestLocked() {
	var oldest time.Time
	for _, startedAt := range t.inflightScopes {
		if oldest.IsZero() || startedAt.Before(oldest) {
			oldest = startedAt
		}
	}
	t.oldestStarted = oldest
}

// begin tracks a new execution. The returned scope must be ended exactly once.
func (t *inFlightTracker) begin(provider string) *inFlightScope {
	if t == nil {
		return nil
	}
	scope := &inFlightScope{tracker: t, provider: provider}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.inflightScopes[scope] = now
	t.count++
	t.byProvider[provider]++
	if t.oldestStarted.IsZero() || now.Before(t.oldestStarted) {
		t.oldestStarted = now
	}
	return scope
}

// snapshot copies the current in-flight state into an immutable view.
func (t *inFlightTracker) snapshot() InFlightSnapshot {
	if t == nil {
		return InFlightSnapshot{ByProvider: map[string]int64{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	byProvider := make(map[string]int64, len(t.byProvider))
	for provider, count := range t.byProvider {
		byProvider[provider] = count
	}
	return InFlightSnapshot{
		InFlight:        t.count,
		OldestStartedAt: t.oldestStarted,
		ByProvider:      byProvider,
	}
}

// InFlightSnapshot returns the current standalone in-flight execution counts.
func (m *Manager) InFlightSnapshot() InFlightSnapshot {
	if m == nil || m.inFlightTracker == nil {
		return InFlightSnapshot{ByProvider: map[string]int64{}}
	}
	return m.inFlightTracker.snapshot()
}

// beginInFlight starts tracking one local execution. The returned scope must be
// ended exactly once; it is nil-safe.
func (m *Manager) beginInFlight(provider string) *inFlightScope {
	if m == nil || m.inFlightTracker == nil {
		return nil
	}
	return m.inFlightTracker.begin(provider)
}

// wrapInFlightStreamResult extends an in-flight scope until the returned stream
// finishes or ctx is canceled, whichever comes first.
func wrapInFlightStreamResult(result *cliproxyexecutor.StreamResult, scope *inFlightScope, ctx context.Context) *cliproxyexecutor.StreamResult {
	if result == nil || scope == nil {
		if scope != nil {
			scope.end()
		}
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		// Ensure scope is always released when this goroutine exits, regardless
		// of which return path is taken. This replaces the previous confusing
		// pattern of both defer and explicit calls.
		defer scope.end()
		if result.Chunks == nil {
			close(out)
			return
		}
		forward := true
		for forward {
			select {
			case <-ctx.Done():
				forward = false
			case chunk, ok := <-result.Chunks:
				if !ok {
					// Channel closed normally; drain is a no-op.
					close(out)
					return
				}
				select {
				case <-ctx.Done():
					forward = false
				case out <- chunk:
				}
			}
		}
		// Context was canceled. Drain the inner channel with a timeout to prevent
		// the producer goroutine from blocking forever on a full buffered channel.
		// If the producer doesn't close within the timeout, we abandon the drain
		// to avoid indefinite blocking (the producer may not respect ctx cancellation).
		drainDone := make(chan struct{})
		go func() {
			for range result.Chunks {
			}
			close(drainDone)
		}()
		select {
		case <-drainDone:
			// Producer finished cleanly.
		case <-time.After(10 * time.Second):
			// Producer didn't close in time; abandon to prevent goroutine leak.
			// The producer will eventually unblock when the executor cleans up
			// or the process exits.
		}
		close(out)
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// inFlightScopeContextKey carries the request in-flight scope to inner
// selection code so the provider attribution can be corrected after pick.
type inFlightScopeContextKey struct{}

// contextWithInFlightScope returns a context carrying the scope, or ctx when
// scope is nil.
func contextWithInFlightScope(ctx context.Context, scope *inFlightScope) context.Context {
	if ctx == nil || scope == nil {
		return ctx
	}
	return context.WithValue(ctx, inFlightScopeContextKey{}, scope)
}

// updateInFlightScopeProvider re-attributes the request scope to the actually
// selected provider after auth selection. It is a no-op without a scope.
func updateInFlightScopeProvider(ctx context.Context, provider string) {
	if ctx == nil {
		return
	}
	scope, ok := ctx.Value(inFlightScopeContextKey{}).(*inFlightScope)
	if !ok || scope == nil {
		return
	}
	scope.setProvider(provider)
}
