package service

import (
	"context"
	"errors"
	"io"
	"time"

	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
)

var (
	ErrInvalidUsageImportSnapshot    = errors.New("invalid usage import snapshot")
	ErrInvalidUsageImportJSON        = errors.New("invalid usage import json")
	ErrUnsupportedUsageImportVersion = errors.New("unsupported usage import version")
)

type UsageProvider interface {
	GetUsageWithFilter(context.Context, servicedto.UsageFilter) (*repodto.StatisticsSnapshot, error)
	GetUsageAggregateWithFilter(context.Context, servicedto.UsageFilter) (*repodto.StatisticsSnapshot, error)
	GetUsageOverview(context.Context, servicedto.UsageFilter) (*servicedto.UsageOverviewSnapshot, error)
	ListUsageEvents(context.Context, servicedto.UsageFilter) (*servicedto.UsageEventsPage, error)
	ListUsageEventFilterOptions(context.Context, servicedto.UsageFilter) (*servicedto.UsageEventFilterOptions, error)
	GetUsageAnalysis(context.Context, servicedto.UsageFilter) (*servicedto.UsageAnalysisSnapshot, error)
	ExportUsageSnapshot(context.Context, io.Writer, time.Time, servicedto.UsageFilter) error
	ImportUsageSnapshot(context.Context, *repodto.StatisticsSnapshot) (*servicedto.UsageImportResult, error)
	ImportUsageSnapshotStream(context.Context, io.Reader) (*servicedto.UsageImportResult, error)
}
