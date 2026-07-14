package repository

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/migration"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func OpenPostgresDatabase(cfg config.Config) (*gorm.DB, error) {
	dsn := strings.TrimSpace(cfg.PostgresDSN)
	if dsn == "" {
		return nil, fmt.Errorf("postgres usage database DSN is required")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open postgres database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("configure postgres usage database: %w", err)
	}
	// Keep usage writes/reads on a small dedicated pool so Shared Pooler
	// connection count and egress stay bounded under request load.
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.AutoMigrate(entities.All()...); err != nil {
		return nil, fmt.Errorf("auto migrate postgres database: %w", err)
	}
	if err := migration.MarkAllAsApplied(db); err != nil {
		return nil, fmt.Errorf("mark schema migrations applied: %w", err)
	}
	if err := migration.Run(db); err != nil {
		return nil, fmt.Errorf("run schema migrations: %w", err)
	}
	return db, nil
}
