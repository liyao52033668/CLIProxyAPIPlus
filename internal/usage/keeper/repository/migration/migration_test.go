package migration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOrderedMigrationsPreservesExecutionOrder(t *testing.T) {
	got := make([]string, 0, len(orderedMigrations()))
	for _, migration := range orderedMigrations() {
		got = append(got, migration.version)
	}
	want := []string{
		"20260503_add_usage_event_redis_fields",
		"20260503_backfill_usage_event_redis_fields",
		"20260503_drop_snapshot_runs",
		"20260504_drop_legacy_snapshot_run_columns",
		"20260504_create_usage_identities",
		"20260504_migrate_usage_identities_metadata",
		"20260504_backfill_usage_event_identity_fields",
		"20260504_backfill_usage_identity_stats",
		"20260504_drop_legacy_metadata_tables",
		"20260504_remove_prefix_usage_identities",
		"20260505_add_usage_identity_lookup_key",
		"20260505_migrate_ai_provider_identities_to_auth_index",
		"20260506_add_usage_performance_indexes",
		"20260507_add_usage_identity_metadata_fields",
		"20260508_add_usage_event_model_alias",
		"20260509_update_usage_identity_quota_fields",
		"20260510_remove_usage_identity_quota_fields",
		"20260714_create_usage_archives",
		"20260714_normalize_usage_event_timestamps",
	}
	if len(got) != len(want) {
		t.Fatalf("expected ordered migrations %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected ordered migrations %v, got %v", want, got)
		}
	}
}

func TestNormalizeUsageEventTimestampsMigrationConvertsOffsetsToUTC(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(testSQLiteDSN(filepath.Join(t.TempDir(), "timestamps.db"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer closeOpenedDatabase(t, db)
	if err := db.AutoMigrate(&entities.UsageEvent{}); err != nil {
		t.Fatalf("auto migrate usage event: %v", err)
	}

	location := time.FixedZone("UTC+8", 8*60*60)
	timestamp := time.Date(2026, 7, 14, 20, 25, 0, 0, location)
	if err := db.Create(&entities.UsageEvent{EventKey: "offset-event", Timestamp: timestamp}).Error; err != nil {
		t.Fatalf("create offset usage event: %v", err)
	}
	if err := normalizeUsageEventTimestampsMigration(db); err != nil {
		t.Fatalf("normalizeUsageEventTimestampsMigration returned error: %v", err)
	}

	start := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 14, 15, 59, 59, 999999999, time.UTC)
	var count int64
	if err := db.Model(&entities.UsageEvent{}).Where("timestamp >= ? AND timestamp <= ?", start, end).Count(&count).Error; err != nil {
		t.Fatalf("count normalized usage event: %v", err)
	}
	if count != 1 {
		t.Fatalf("normalized event count = %d, want 1", count)
	}

	var event entities.UsageEvent
	if err := db.Where("event_key = ?", "offset-event").First(&event).Error; err != nil {
		t.Fatalf("load normalized usage event: %v", err)
	}
	if !event.Timestamp.Equal(timestamp) || event.Timestamp.Location() != time.UTC {
		t.Fatalf("normalized timestamp = %s (%s), want UTC instant %s", event.Timestamp, event.Timestamp.Location(), timestamp.UTC())
	}
}

func TestOpenDatabaseRunsSchemaMigrationsAndAddsUsageEventRedisFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacyRedisUsageTables(t, dbPath)

	db := openMigratedDatabase(t, dbPath)
	defer closeOpenedDatabase(t, db)

	if !db.Migrator().HasTable("schema_migrations") {
		t.Fatal("expected schema_migrations table to exist")
	}
	for _, column := range []string{"provider", "endpoint", "auth_type", "request_id"} {
		if !db.Migrator().HasColumn(&entities.UsageEvent{}, column) {
			t.Fatalf("expected usage_events.%s column to exist", column)
		}
	}
	if !db.Migrator().HasColumn(&entities.UsageIdentity{}, "lookup_key") {
		t.Fatal("expected usage_identities.lookup_key column to exist")
	}
	if !db.Migrator().HasTable(&entities.UsageHourlyAggregate{}) || !db.Migrator().HasTable(&entities.UsageEventKey{}) {
		t.Fatal("expected usage archive tables to exist")
	}

	var versions []string
	if err := db.Table("schema_migrations").Order("version asc").Pluck("version", &versions).Error; err != nil {
		t.Fatalf("load schema migrations: %v", err)
	}
	expected := []string{
		"20260503_add_usage_event_redis_fields",
		"20260503_backfill_usage_event_redis_fields",
		"20260503_drop_snapshot_runs",
		"20260504_backfill_usage_event_identity_fields",
		"20260504_backfill_usage_identity_stats",
		"20260504_create_usage_identities",
		"20260504_drop_legacy_metadata_tables",
		"20260504_drop_legacy_snapshot_run_columns",
		"20260504_migrate_usage_identities_metadata",
		"20260504_remove_prefix_usage_identities",
		"20260505_add_usage_identity_lookup_key",
		"20260505_migrate_ai_provider_identities_to_auth_index",
		"20260506_add_usage_performance_indexes",
		"20260507_add_usage_identity_metadata_fields",
		"20260508_add_usage_event_model_alias",
		"20260509_update_usage_identity_quota_fields",
		"20260510_remove_usage_identity_quota_fields",
		"20260714_create_usage_archives",
		"20260714_normalize_usage_event_timestamps",
	}
	if len(versions) != len(expected) {
		t.Fatalf("expected migration versions %v, got %v", expected, versions)
	}
	for i := range expected {
		if versions[i] != expected[i] {
			t.Fatalf("expected migration versions %v, got %v", expected, versions)
		}
	}
}

func TestOpenDatabaseMigrationsAreIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacyRedisUsageTables(t, dbPath)

	db := openMigratedDatabase(t, dbPath)
	closeOpenedDatabase(t, db)

	db = openMigratedDatabase(t, dbPath)
	defer closeOpenedDatabase(t, db)

	var count int64
	if err := db.Table("schema_migrations").Count(&count).Error; err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	expectedCount := int64(len(orderedMigrations()))
	if count != expectedCount {
		t.Fatalf("expected %d applied migrations after reopening database, got %d", expectedCount, count)
	}
}

// func TestOpenDatabaseLogsSchemaMigrations(t *testing.T) {
// 	logs := captureMigrationLogs(t, logrus.InfoLevel)
// 	dbPath := filepath.Join(t.TempDir(), "legacy.db")
// 	seedLegacyRedisUsageTables(t, dbPath)

// 	db := openMigratedDatabase(t, dbPath)
// 	closeOpenedDatabase(t, db)

// 	db = openMigratedDatabase(t, dbPath)
// 	closeOpenedDatabase(t, db)

// 	content := logs.String()
// 	for _, want := range []string{
// 		"level=info",
// 		"msg=\"schema migration started\"",
// 		"msg=\"schema migration applied\"",
// 		"msg=\"schema migration skipped\"",
// 		"version=20260503_add_usage_event_redis_fields",
// 		"version=20260504_migrate_usage_identities_metadata",
// 		"version=20260504_drop_legacy_metadata_tables",
// 	} {
// 		if !strings.Contains(content, want) {
// 			t.Fatalf("expected migration logs to contain %q, got:\n%s", want, content)
// 		}
// 	}
// }

func TestMarkSchemaMigrationAppliedIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(testSQLiteDSN(filepath.Join(t.TempDir(), "app.db"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer closeOpenedDatabase(t, db)

	if err := createSchemaMigrationsTable(db); err != nil {
		t.Fatalf("create schema_migrations table: %v", err)
	}

	appliedAt := schemaMigration{Version: "test_version"}.AppliedAt
	if err := markSchemaMigrationApplied(db, "test_version", appliedAt); err != nil {
		t.Fatalf("first mark schema migration applied: %v", err)
	}
	if err := markSchemaMigrationApplied(db, "test_version", appliedAt); err != nil {
		t.Fatalf("second mark schema migration applied: %v", err)
	}

	var count int64
	if err := db.Table("schema_migrations").Where("version = ?", "test_version").Count(&count).Error; err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one applied schema migration, got %d", count)
	}
}

func TestRunSchemaMigrationLogsErrors(t *testing.T) {
	logs := captureMigrationLogs(t, logrus.InfoLevel)
	db, err := gorm.Open(sqlite.Open(testSQLiteDSN(filepath.Join(t.TempDir(), "app.db"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer closeOpenedDatabase(t, db)
	if err := createSchemaMigrationsTable(db); err != nil {
		t.Fatalf("create schema_migrations table: %v", err)
	}

	err = runSchemaMigration(db, databaseMigration{
		version: "test_failure",
		run: func(*gorm.DB) error {
			return fmt.Errorf("boom")
		},
	})
	if err == nil {
		t.Fatal("expected migration error")
	}

	content := logs.String()
	for _, want := range []string{
		"level=info",
		"msg=\"schema migration started\"",
		"version=test_failure",
		"level=error",
		"msg=\"schema migration failed\"",
		"error=boom",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected migration error logs to contain %q, got:\n%s", want, content)
		}
	}
}
