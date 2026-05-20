package repository

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
)

func TestOpenPostgresDatabaseRejectsEmptyDSN(t *testing.T) {
	_, err := OpenPostgresDatabase(config.Config{PostgresDSN: ""})
	if err == nil {
		t.Fatal("expected error for empty Postgres DSN")
	}
}

func TestOpenPostgresDatabaseBuildsGormDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}

	dsn := os.Getenv("USAGE_KEEPER_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skipping Postgres integration test without USAGE_KEEPER_TEST_POSTGRES_DSN")
	}

	db, err := OpenPostgresDatabase(config.Config{PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("OpenPostgresDatabase returned error: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	defer sqlDB.Close()

	if !db.Migrator().HasTable("usage_events") {
		t.Fatal("expected usage_events table to exist")
	}
	if !db.Migrator().HasTable("schema_migrations") {
		t.Fatal("expected schema_migrations table to exist")
	}
}
