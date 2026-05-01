package usage

import (
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsagePersister loads and saves usage statistics for a specific storage backend.
type UsagePersister interface {
	// LoadUsage returns a previously saved usage snapshot, or nil if none exists.
	LoadUsage() (*StatisticsSnapshot, error)
	// SaveUsage persists the current usage snapshot.
	SaveUsage(snapshot *StatisticsSnapshot) error
}

const defaultSaveInterval = 5 * time.Minute

// persistenceStarted acts as a one-time guard, replacing sync.Once to allow
// conditional restarts (e.g. hot-reload).
var persistenceStarted atomic.Bool

// StartPersistence begins periodic auto-save of the usage statistics.
// It loads any existing data on startup and saves every interval thereafter.
// If persister is nil, persistence is a no-op.
// Calling this multiple times is safe; work only runs once unless ResetPersistence
// is called first.
func StartPersistence(persister UsagePersister, interval time.Duration) {
	if interval <= 0 {
		interval = defaultSaveInterval
	}
	if persister == nil {
		return
	}

	if !persistenceStarted.CompareAndSwap(false, true) {
		return
	}

	// Load existing snapshot
	snapshot, err := persister.LoadUsage()
	if err != nil {
		log.WithError(err).Warn("failed to load usage statistics")
	} else if snapshot != nil {
		result := defaultRequestStatistics.MergeSnapshot(*snapshot)
		log.Infof("loaded usage statistics (added=%d, skipped=%d)", result.Added, result.Skipped)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			snapshot := defaultRequestStatistics.Snapshot()
			if err := persister.SaveUsage(&snapshot); err != nil {
				log.WithError(err).Error("failed to save usage statistics")
			}
		}
	}()
}

// ResetPersistence allows the persistence loop to be started again after a
// Stop / reconfigure event. Called before a second StartPersistence.
func ResetPersistence() {
	persistenceStarted.Store(false)
}

// SaveNow forces an immediate save of the current usage statistics.
func SaveNow(persister UsagePersister) error {
	if persister == nil {
		return nil
	}
	snapshot := defaultRequestStatistics.Snapshot()
	return persister.SaveUsage(&snapshot)
}
