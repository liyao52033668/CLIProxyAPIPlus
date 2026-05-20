package repository

import (
	"fmt"
	"strings"

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
