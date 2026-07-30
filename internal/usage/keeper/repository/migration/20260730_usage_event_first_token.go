package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"

	"gorm.io/gorm"
)

func addUsageEventFirstTokenMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&entities.UsageEvent{}) {
		return nil
	}
	if tx.Migrator().HasColumn(&entities.UsageEvent{}, "first_token_ms") {
		return nil
	}
	if err := tx.Exec("ALTER TABLE usage_events ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0").Error; err != nil {
		return fmt.Errorf("add usage_events.first_token_ms column: %w", err)
	}
	return nil
}
