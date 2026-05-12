package migration

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/entities"

	"gorm.io/gorm"
)

func addUsageIdentityMetadataFieldsMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&entities.UsageIdentity{}) {
		return nil
	}

	columns := []struct {
		name string
		sql  string
	}{
		{name: "prefix", sql: "ALTER TABLE usage_identities ADD COLUMN prefix TEXT"},
		{name: "account_id", sql: "ALTER TABLE usage_identities ADD COLUMN account_id TEXT"},
		{name: "active_start", sql: "ALTER TABLE usage_identities ADD COLUMN active_start DATETIME"},
		{name: "active_until", sql: "ALTER TABLE usage_identities ADD COLUMN active_until DATETIME"},
		{name: "plan_type", sql: "ALTER TABLE usage_identities ADD COLUMN plan_type TEXT"},
		{name: "limit_reached", sql: "ALTER TABLE usage_identities ADD COLUMN limit_reached NUMERIC"},
		{name: "primary_window_used_percent", sql: "ALTER TABLE usage_identities ADD COLUMN primary_window_used_percent INTEGER"},
		{name: "primary_window_limit_seconds", sql: "ALTER TABLE usage_identities ADD COLUMN primary_window_limit_seconds INTEGER"},
		{name: "primary_window_reset_seconds", sql: "ALTER TABLE usage_identities ADD COLUMN primary_window_reset_seconds INTEGER"},
		{name: "primary_window_reset_at", sql: "ALTER TABLE usage_identities ADD COLUMN primary_window_reset_at DATETIME"},
		{name: "secondary_window_used_percent", sql: "ALTER TABLE usage_identities ADD COLUMN secondary_window_used_percent INTEGER"},
		{name: "secondary_window_limit_seconds", sql: "ALTER TABLE usage_identities ADD COLUMN secondary_window_limit_seconds INTEGER"},
		{name: "secondary_window_reset_seconds", sql: "ALTER TABLE usage_identities ADD COLUMN secondary_window_reset_seconds INTEGER"},
		{name: "secondary_window_reset_at", sql: "ALTER TABLE usage_identities ADD COLUMN secondary_window_reset_at DATETIME"},
	}

	for _, column := range columns {
		if tx.Migrator().HasColumn(&entities.UsageIdentity{}, column.name) {
			continue
		}
		if err := tx.Exec(column.sql).Error; err != nil {
			return fmt.Errorf("add usage_identities.%s column: %w", column.name, err)
		}
	}
	return nil
}
