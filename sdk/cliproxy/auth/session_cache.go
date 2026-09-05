// Package auth provides authentication management, scheduling, and session handling for CLIProxyAPI.
package auth

import (
	"container/list"
	"sync"
	"time"
)

const defaultMaxSessionEntries = 65536

// sessionEntry stores auth binding with expiration.
type sessionEntry struct {
	authID    string
	expiresAt time.Time
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup
// and bounded memory via LRU capacity eviction.
type SessionCache struct {
	mu               sync.RWMutex
	entries          map[string]sessionEntry
	evictionOrder    *list.List
	evictionElements map[string]*list.Element
	maxEntries       int
	ttl              time.Duration
	stopCh           chan struct{}
	stopOnce         sync.Once
}

// NewSessionCache creates a cache with the specified TTL and the default capacity.
// A background goroutine periodically cleans expired entries.
func NewSessionCache(ttl time.Duration) *SessionCache {
	return NewSessionCacheWithCapacity(ttl, defaultMaxSessionEntries)
}

// NewSessionCacheWithCapacity creates a cache with the specified TTL and capacity.
// A maxEntries value of <= 0 falls back to the default capacity.
func NewSessionCacheWithCapacity(ttl time.Duration, maxEntries int) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxSessionEntries
	}
	c := &SessionCache{
		entries:          make(map[string]sessionEntry),
		evictionOrder:    list.New(),
		evictionElements: make(map[string]*list.Element),
		maxEntries:       maxEntries,
		ttl:              ttl,
		stopCh:           make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// touchLRULocked moves the session to the back of the eviction order.
func (c *SessionCache) touchLRULocked(sessionID string) {
	if elem, ok := c.evictionElements[sessionID]; ok {
		c.evictionOrder.MoveToBack(elem)
		return
	}
	c.evictionElements[sessionID] = c.evictionOrder.PushBack(sessionID)
}

// removeLRULocked drops the session from the eviction order.
func (c *SessionCache) removeLRULocked(sessionID string) {
	if elem, ok := c.evictionElements[sessionID]; ok {
		c.evictionOrder.Remove(elem)
		delete(c.evictionElements, sessionID)
	}
}

// evictExcessLocked evicts least-recently-used sessions until within capacity.
func (c *SessionCache) evictExcessLocked() {
	for len(c.entries) > c.maxEntries {
		oldest := c.evictionOrder.Front()
		if oldest == nil {
			break
		}
		sessionID, _ := oldest.Value.(string)
		c.evictionOrder.Remove(oldest)
		delete(c.evictionElements, sessionID)
		delete(c.entries, sessionID)
	}
}

// Get retrieves the auth ID bound to a session, if still valid.
// Does NOT refresh the TTL or touch LRU recency on access; use GetAndRefresh
// for accesses that should extend the binding lifetime.
func (c *SessionCache) Get(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[sessionID]
	valid := ok && now.Before(entry.expiresAt)
	var authID string
	if valid {
		authID = entry.authID
	}
	c.mu.RUnlock()
	if valid {
		return authID, true
	}
	if !ok {
		return "", false
	}
	// Slow path: the entry existed but looked expired; re-check under the
	// write lock and drop it if it has really expired.
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok = c.entries[sessionID]
	if !ok {
		return "", false
	}
	if now.Before(entry.expiresAt) {
		return entry.authID, true
	}
	c.removeLRULocked(sessionID)
	delete(c.entries, sessionID)
	return "", false
}

// GetAndRefresh retrieves the auth ID bound to a session and refreshes TTL on hit.
// This extends the binding lifetime for active sessions.
func (c *SessionCache) GetAndRefresh(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok {
		return "", false
	}
	if now.After(entry.expiresAt) {
		c.removeLRULocked(sessionID)
		delete(c.entries, sessionID)
		return "", false
	}
	// Refresh TTL on successful access
	entry.expiresAt = now.Add(c.ttl)
	c.entries[sessionID] = entry
	c.touchLRULocked(sessionID)
	return entry.authID, true
}

// Set binds a session to an auth ID with TTL refresh.
func (c *SessionCache) Set(sessionID, authID string) {
	if sessionID == "" || authID == "" {
		return
	}
	c.mu.Lock()
	c.entries[sessionID] = sessionEntry{
		authID:    authID,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.touchLRULocked(sessionID)
	c.evictExcessLocked()
	c.mu.Unlock()
}

// Invalidate removes a specific session binding.
func (c *SessionCache) Invalidate(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	c.removeLRULocked(sessionID)
	delete(c.entries, sessionID)
	c.mu.Unlock()
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	for sid, entry := range c.entries {
		if entry.authID == authID {
			c.removeLRULocked(sid)
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
}

// Stop terminates the background cleanup goroutine.
func (c *SessionCache) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

func (c *SessionCache) cleanupLoop() {
	ticker := time.NewTicker(c.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

func (c *SessionCache) cleanup() {
	now := time.Now()
	c.mu.Lock()
	for sid, entry := range c.entries {
		if now.After(entry.expiresAt) {
			c.removeLRULocked(sid)
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
}
