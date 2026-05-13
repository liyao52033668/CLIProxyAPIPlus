package service

import (
	"context"
	"errors"

	repodto "github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/repository/dto"
	servicedto "github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/service/dto"
)

var ErrInvalidUsageImportSnapshot = errors.New("invalid usage import snapshot")

type UsageProvider interface {
	GetUsageWithFilter(context.Context, servicedto.UsageFilter) (*repodto.StatisticsSnapshot, error)
	GetUsageOverview(context.Context, servicedto.UsageFilter) (*servicedto.UsageOverviewSnapshot, error)
	ListUsageEvents(context.Context, servicedto.UsageFilter) (*servicedto.UsageEventsPage, error)
	ListUsageEventFilterOptions(context.Context, servicedto.UsageFilter) (*servicedto.UsageEventFilterOptions, error)
	GetUsageAnalysis(context.Context, servicedto.UsageFilter) (*servicedto.UsageAnalysisSnapshot, error)
	ImportUsageSnapshot(context.Context, *repodto.StatisticsSnapshot) (*servicedto.UsageImportResult, error)
}
