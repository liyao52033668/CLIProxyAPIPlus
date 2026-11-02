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
	if !tx.Migrator().HasColumn(&entities.UsageEvent{}, "fail_status_code") {
		if err := tx.Migrator().AddColumn(&entities.UsageEvent{}, "fail_status_code"); err != nil {
			return fmt.Errorf("add usage_events.fail_status_code column: %w", err)
		}
	}
	if !tx.Migrator().HasColumn(&entities.UsageEvent{}, "error_message") {
		if err := tx.Migrator().AddColumn(&entities.UsageEvent{}, "error_message"); err != nil {
			return fmt.Errorf("add usage_events.error_message column: %w", err)
		}
	}
	return nil
}
