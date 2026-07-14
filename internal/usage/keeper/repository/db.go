package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/migration"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func OpenDatabase(cfg config.Config) (*gorm.DB, error) {
	databaseExists, err := sqliteDatabaseFileExists(cfg.SQLitePath)
	if err != nil {
		return nil, err
	}
	dsn := sqliteDSN(cfg.SQLitePath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open sqlite database %s: %w", filepath.Clean(cfg.SQLitePath), err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("configure sqlite database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, fmt.Errorf("enable sqlite WAL: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	if err := db.Exec("PRAGMA foreign_keys=ON").Error; err != nil {
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	hasTables, err := sqliteDatabaseHasTables(db)
	if err != nil {
		return nil, err
	}
	if !databaseExists || !hasTables {
		if err := db.AutoMigrate(entities.All()...); err != nil {
			return nil, fmt.Errorf("auto migrate fresh database: %w", err)
		}
		if err := migration.MarkAllAsApplied(db); err != nil {
			return nil, fmt.Errorf("mark schema migrations applied: %w", err)
		}
		return db, nil
	}

	if err := migration.Run(db); err != nil {
		return nil, fmt.Errorf("run schema migrations: %w", err)
	}

	return db, nil
}

func sqliteDSN(path string) string {
	trimmed := strings.TrimSpace(path)
	if strings.Contains(trimmed, "?") {
		return trimmed
	}
	return trimmed + "?_busy_timeout=5000&_foreign_keys=on"
}

func sqliteDatabaseFileExists(path string) (bool, error) {
	trimmed := strings.TrimSpace(path)
	if before, _, ok := strings.Cut(trimmed, "?"); ok {
		trimmed = before
	}
	if trimmed == "" || trimmed == ":memory:" {
		return false, nil
	}
	_, err := os.Stat(trimmed)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("check sqlite database %s: %w", filepath.Clean(trimmed), err)
}

func sqliteDatabaseHasTables(db *gorm.DB) (bool, error) {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&count).Error; err != nil {
		return false, fmt.Errorf("check sqlite database tables: %w", err)
	}
	return count > 0, nil
}

func InsertUsageEvents(db *gorm.DB, events []entities.UsageEvent) (int, int, error) {
	if db == nil {
		return 0, 0, fmt.Errorf("database is nil")
	}
	if len(events) == 0 {
		return 0, 0, nil
	}

	normalizedEvents := append([]entities.UsageEvent(nil), events...)
	for i := range normalizedEvents {
		normalizedEvents[i].Timestamp = normalizedEvents[i].Timestamp.UTC()
	}

	inserted, err := insertUsageEventsWithArchiveKeys(db, normalizedEvents)
	if err != nil {
		return 0, 0, err
	}
	return inserted, len(events) - inserted, nil
}

// CleanupStorage aggregates identity statistics, archives expired request details, cleans the Redis inbox, and vacuums the database.
func CleanupStorage(db *gorm.DB, now time.Time) (dto.StorageCleanupResult, error) {
	if db == nil {
		return dto.StorageCleanupResult{}, fmt.Errorf("database is nil")
	}
	if err := AggregateUsageIdentityStats(context.Background(), db, now); err != nil {
		return dto.StorageCleanupResult{}, err
	}
	usageResult, err := CleanupUsageEvents(db, now)
	if err != nil {
		return dto.StorageCleanupResult{UsageEvents: usageResult}, err
	}
	redisResult, err := CleanupRedisUsageInbox(db, now)
	if err != nil {
		return dto.StorageCleanupResult{UsageEvents: usageResult, RedisInbox: redisResult}, err
	}
	result := dto.StorageCleanupResult{UsageEvents: usageResult, RedisInbox: redisResult}
	deletedRows := usageResult.ArchivedEvents + usageResult.DeletedEventKeys + redisResult.ProcessedDeleted + redisResult.FailedDeleted
	if shouldVacuumStorage(db.Dialector.Name(), now, deletedRows) {
		if err := db.Exec("VACUUM").Error; err != nil {
			return result, err
		}
		result.Vacuumed = true
	}
	return result, nil
}

func shouldVacuumStorage(dialect string, now time.Time, deletedRows int64) bool {
	return dialect == "sqlite" && now.In(time.Local).Day() == 1 && deletedRows > 0
}

func Vacuum(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	return db.Exec("VACUUM").Error
}
