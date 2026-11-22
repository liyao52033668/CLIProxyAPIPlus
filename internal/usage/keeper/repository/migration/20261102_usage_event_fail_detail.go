package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"

	"gorm.io/gorm"
)

// addUsageEventFailDetailMigration adds the per-event upstream failure detail
// columns used by the request event list error dialog.
func addUsageEventFailDetailMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&entities.UsageEvent{}) {
		return nil
	}
	// Check if fail_status_code column exists using GORM's HasColumn (compatible with all databases)
	if !tx.Migrator().HasColumn(&entities.UsageEvent{}, "fail_status_code") {
		if err := tx.Migrator().AddColumn(&entities.UsageEvent{}, "fail_status_code"); err != nil {
			// Ignore error if column already exists (race condition)
			if !isColumnAlreadyExistsError(err) {
				return fmt.Errorf("add usage_events.fail_status_code column: %w", err)
			}
		}
	}
	// Check if error_message column exists using GORM's HasColumn (compatible with all databases)
	if !tx.Migrator().HasColumn(&entities.UsageEvent{}, "error_message") {
		if err := tx.Migrator().AddColumn(&entities.UsageEvent{}, "error_message"); err != nil {
			// Ignore error if column already exists (race condition)
			if !isColumnAlreadyExistsError(err) {
				return fmt.Errorf("add usage_events.error_message column: %w", err)
			}
		}
	}
	return nil
}

// isColumnAlreadyExistsError checks if the error is a "column already exists" error
func isColumnAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return contains(errMsg, "already exists") || contains(errMsg, "42701") || contains(errMsg, "duplicate column")
}

// contains checks if substr is in s (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}
