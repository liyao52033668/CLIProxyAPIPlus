package service

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"gorm.io/gorm"
)

type LocalSyncService struct {
	db *gorm.DB
}

func NewLocalSyncService(db *gorm.DB) *LocalSyncService {
	return &LocalSyncService{db: db}
}

func (s *LocalSyncService) CleanupStorage(ctx context.Context) error {
	_, err := repository.CleanupStorage(s.db, time.Now())
	return err
}

func (s *LocalSyncService) InsertUsageEvents(events []entities.UsageEvent) (int, int, error) {
	return repository.InsertUsageEvents(s.db, events)
}

func (s *LocalSyncService) SyncMetadata(ctx context.Context) error {
	return nil
}
