package migration

import (
	"fmt"

	"gorm.io/gorm"
)

func dropSnapshotRunsMigration(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("snapshot_runs") {
		return nil
	}
	if err := tx.Exec("DROP TABLE IF EXISTS snapshot_runs").Error; err != nil {
		return fmt.Errorf("drop snapshot_runs table: %w", err)
	}
	return nil
}

func dropLegacySnapshotRunColumnsMigration(tx *gorm.DB) error {
	for _, indexSpec := range []struct {
		tableName string
		indexName string
	}{
		{tableName: "usage_events", indexName: "idx_usage_events_snapshot_run_id"},
		{tableName: "redis_usage_inboxes", indexName: "idx_redis_usage_inboxes_snapshot_run_id"},
	} {
		if err := dropIndexIfExists(tx, indexSpec.tableName, indexSpec.indexName); err != nil {
			return err
		}
	}
	if err := dropColumnIfExists(tx, "usage_events", "snapshot_run_id"); err != nil {
		return err
	}
	if err := dropColumnIfExists(tx, "redis_usage_inboxes", "snapshot_run_id"); err != nil {
		return err
	}
	return nil
}
