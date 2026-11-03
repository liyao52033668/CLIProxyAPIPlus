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
	// Check if fail_status_code column exists using raw query to avoid Gorm's cache
	var hasColumn bool
	if err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'usage_events'
			AND column_name = 'fail_status_code'
		)
	`).Scan(&hasColumn).Error; err != nil {
		return fmt.Errorf("check fail_status_code column existence: %w", err)
	}
	if !hasColumn {
		if err := tx.Migrator().AddColumn(&entities.UsageEvent{}, "fail_status_code"); err != nil {
			// Ignore error if column already exists (race condition)
			if !isColumnAlreadyExistsError(err) {
				return fmt.Errorf("add usage_events.fail_status_code column: %w", err)
			}
		}
	}
	// Check if error_message column exists using raw query to avoid Gorm's cache
	var hasErrorMessage bool
	if err := tx.Raw(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'usage_events'
			AND column_name = 'error_message'
		)
	`).Scan(&hasErrorMessage).Error; err != nil {
		return fmt.Errorf("check error_message column existence: %w", err)
	}
	if !hasErrorMessage {
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
