package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"

	"gorm.io/gorm"
)

func addUsageEventModelAliasMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&entities.UsageEvent{}) {
		return nil
	}
	if tx.Migrator().HasColumn(&entities.UsageEvent{}, "model_alias") {
		return nil
	}
	if err := tx.Exec("ALTER TABLE usage_events ADD COLUMN model_alias TEXT").Error; err != nil {
		return fmt.Errorf("add usage_events.model_alias column: %w", err)
	}
	return nil
}
