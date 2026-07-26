package service

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"gorm.io/gorm"
)

type ListUsageIdentitiesRequest struct {
	AuthType *entities.UsageIdentityAuthType
	Page     int
	PageSize int
}

type ListUsageIdentitiesResponse struct {
	Items []entities.UsageIdentity
	Total int64
}

type UsageIdentityProvider interface {
	ListUsageIdentities(context.Context) ([]entities.UsageIdentity, error)
	ListActiveUsageIdentities(context.Context) ([]entities.UsageIdentity, error)
	ListActiveUsageIdentitiesPage(context.Context, ListUsageIdentitiesRequest) (ListUsageIdentitiesResponse, error)
}

type usageIdentityService struct {
	db *gorm.DB
}

func NewUsageIdentityService(db *gorm.DB) UsageIdentityProvider {
	return &usageIdentityService{db: db}
}

func (s *usageIdentityService) ListUsageIdentities(ctx context.Context) ([]entities.UsageIdentity, error) {
	// The identities page needs full history, including deleted identities, for deleted status and stats.
	return repository.ListUsageIdentities(ctx, s.db)
}

func (s *usageIdentityService) ListActiveUsageIdentities(ctx context.Context) ([]entities.UsageIdentity, error) {
	// Source resolution and filtering only need active identities; push the filter into repository SQL.
	return repository.ListActiveUsageIdentities(ctx, s.db)
}

func (s *usageIdentityService) ListActiveUsageIdentitiesPage(ctx context.Context, request ListUsageIdentitiesRequest) (ListUsageIdentitiesResponse, error) {
	items, total, err := repository.ListActiveUsageIdentitiesPage(ctx, s.db, repository.ListUsageIdentitiesPageRequest{
		AuthType: request.AuthType,
		Page:     request.Page,
		PageSize: request.PageSize,
	})
	if err != nil {
		return ListUsageIdentitiesResponse{}, err
	}
	return ListUsageIdentitiesResponse{Items: items, Total: total}, nil
}
