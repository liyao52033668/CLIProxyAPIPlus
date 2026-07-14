package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"gorm.io/gorm"
)

func createUsageArchivesMigration(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&entities.UsageHourlyAggregate{}, &entities.UsageEventKey{}); err != nil {
		return fmt.Errorf("create usage archive tables: %w", err)
	}
	return nil
}

func normalizeUsageEventTimestampsMigration(tx *gorm.DB) error {
	if tx.Dialector.Name() != "sqlite" || !tx.Migrator().HasTable(&entities.UsageEvent{}) {
		return nil
	}
	if err := tx.Exec(`UPDATE usage_events
		SET timestamp = strftime('%Y-%m-%d %H:%M:%f+00:00', timestamp)
		WHERE timestamp IS NOT NULL`).Error; err != nil {
		return fmt.Errorf("normalize usage event timestamps: %w", err)
	}
	return nil
}
