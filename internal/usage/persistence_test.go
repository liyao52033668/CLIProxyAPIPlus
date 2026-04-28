package usage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryUsagePersister struct {
	mu       sync.Mutex
	snapshot *StatisticsSnapshot
	loadErr  error
	saveErr  error
}

func (m *memoryUsagePersister) LoadUsage() (*StatisticsSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.snapshot == nil {
		return nil, nil
	}
	copied := *m.snapshot
	return &copied, nil
}

func (m *memoryUsagePersister) SaveUsage(snapshot *StatisticsSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	if snapshot == nil {
		m.snapshot = nil
		return nil
	}
	copied := *snapshot
	m.snapshot = &copied
	return nil
}

func TestStartPersistenceIsIdempotent(t *testing.T) {
	ResetPersistence()

	persister := &memoryUsagePersister{}
	StartPersistence(persister, 10*time.Millisecond)
	StartPersistence(persister, 10*time.Millisecond)

	if !persistenceStarted.Load() {
		t.Fatalf("persistenceStarted = false, want true")
	}
}

func TestSaveNowPersistsSnapshot(t *testing.T) {
	ResetPersistence()

	persister := &memoryUsagePersister{}
	if err := SaveNow(persister); err != nil {
		t.Fatalf("SaveNow error: %v", err)
	}

	loaded, err := persister.LoadUsage()
	if err != nil {
		t.Fatalf("LoadUsage error: %v", err)
	}
	if loaded == nil {
		t.Fatalf("LoadUsage snapshot is nil")
	}
}

func TestSaveNowReturnsError(t *testing.T) {
	ResetPersistence()

	persister := &memoryUsagePersister{saveErr: errors.New("save failed")}
	if err := SaveNow(persister); err == nil {
		t.Fatalf("SaveNow error = nil, want non-nil")
	}
}
