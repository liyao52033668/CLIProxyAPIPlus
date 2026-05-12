package migration

import (
	"fmt"

	"gorm.io/gorm"
)

func addUsagePerformanceIndexesMigration(db *gorm.DB) error {
	if !hasMigrationColumns(db, "usage_events", "timestamp", "model", "source", "auth_index", "auth_type", "provider", "api_group_key") ||
		!hasMigrationColumns(db, "redis_usage_inboxes", "status", "processed_at", "updated_at", "usage_event_key") ||
		!hasMigrationColumns(db, "usage_identities", "auth_type", "type", "name") {
		return fmt.Errorf("missing required schema for performance index migration")
	}

	statements := []string{
		"DROP INDEX IF EXISTS idx_usage_events_timestamp",
		"DROP INDEX IF EXISTS idx_usage_events_api_group_key",
		"DROP INDEX IF EXISTS idx_usage_events_source",
		"DROP INDEX IF EXISTS idx_usage_events_auth_index",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp_id ON usage_events(timestamp DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_model ON usage_events(TRIM(model))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_source ON usage_events(TRIM(source))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_auth_index ON usage_events(TRIM(auth_index))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_provider ON usage_events(TRIM(provider))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_auth_type ON usage_events(TRIM(auth_type))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_trim_api_group_key ON usage_events(TRIM(api_group_key))",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_auth_type_auth_index_id ON usage_events(auth_type, auth_index, id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_events_auth_type_source_id ON usage_events(auth_type, source, id)",
		"DROP INDEX IF EXISTS idx_redis_usage_inboxes_status",
		"DROP INDEX IF EXISTS idx_redis_usage_inboxes_queue_key",
		"DROP INDEX IF EXISTS idx_redis_usage_inboxes_message_hash",
		"DROP INDEX IF EXISTS idx_redis_usage_inboxes_usage_event_key",
		"DROP INDEX IF EXISTS idx_redis_usage_inboxes_popped_at",
		"CREATE INDEX IF NOT EXISTS idx_redis_usage_inboxes_status_id ON redis_usage_inboxes(status, id)",
		"CREATE INDEX IF NOT EXISTS idx_redis_usage_inboxes_status_processed_at ON redis_usage_inboxes(status, processed_at)",
		"CREATE INDEX IF NOT EXISTS idx_redis_usage_inboxes_status_updated_at ON redis_usage_inboxes(status, updated_at)",
		"CREATE INDEX IF NOT EXISTS idx_redis_usage_inboxes_status_usage_event_key ON redis_usage_inboxes(status, usage_event_key)",
		"DROP INDEX IF EXISTS idx_usage_identities_auth_type",
		"DROP INDEX IF EXISTS idx_usage_identities_auth_type_name",
		"DROP INDEX IF EXISTS idx_usage_identities_identity",
		"DROP INDEX IF EXISTS idx_usage_identities_is_deleted",
		"DROP INDEX IF EXISTS idx_usage_identities_last_aggregated_usage_event_id",
		"DROP INDEX IF EXISTS idx_usage_identities_deleted_at",
		"CREATE INDEX IF NOT EXISTS idx_usage_identities_auth_type_name_id ON usage_identities(auth_type, name, id)",
		"CREATE INDEX IF NOT EXISTS idx_usage_identities_auth_type_type ON usage_identities(auth_type, type)",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func hasMigrationColumns(db *gorm.DB, table string, columns ...string) bool {
	if !db.Migrator().HasTable(table) {
		return false
	}
	for _, column := range columns {
		if !db.Migrator().HasColumn(table, column) {
			return false
		}
	}
	return true
}
