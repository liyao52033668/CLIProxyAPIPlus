package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/repository"
	repodto "github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/repository/dto"
)

func TestUsageServiceImportUsageSnapshotInsertsAndDedupesEvents(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-service-import.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	provider := NewUsageService(db)
	timestamp := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	snapshot := &repodto.StatisticsSnapshot{
		APIs: map[string]repodto.APISnapshot{
			"provider-a": {
				Models: map[string]repodto.ModelSnapshot{
					"claude-sonnet": {
						TotalRequests: 1,
						SuccessCount:  1,
						TotalTokens:   18,
						Details: []repodto.RequestDetail{{
							Timestamp: timestamp,
							LatencyMS: 321,
							Source:    "source-a",
							AuthIndex: "2",
							Failed:    false,
							Tokens: repodto.TokenStats{
								InputTokens:     10,
								OutputTokens:    5,
								ReasoningTokens: 2,
								CachedTokens:    1,
								TotalTokens:     18,
							},
						}},
					},
				},
			},
		},
	}

	result, err := provider.ImportUsageSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("ImportUsageSnapshot returned error: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("expected first import to insert one event, got %+v", result)
	}
	if result.TotalRequests != 1 || result.FailedCount != 0 {
		t.Fatalf("expected totals after first import, got %+v", result)
	}

	result, err = provider.ImportUsageSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("ImportUsageSnapshot second call returned error: %v", err)
	}
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("expected second import to dedupe the event, got %+v", result)
	}
	if result.TotalRequests != 1 || result.FailedCount != 0 {
		t.Fatalf("expected totals after second import, got %+v", result)
	}
}

func TestUsageServiceImportUsageSnapshotRejectsAggregateOnlySnapshot(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-service-import-invalid.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	provider := NewUsageService(db)
	snapshot := &repodto.StatisticsSnapshot{
		APIs: map[string]repodto.APISnapshot{
			"provider-a": {
				Models: map[string]repodto.ModelSnapshot{
					"claude-sonnet": {
						TotalRequests: 2,
						SuccessCount:  2,
						TotalTokens:   30,
					},
				},
			},
		},
	}

	_, err = provider.ImportUsageSnapshot(context.Background(), snapshot)
	if !errors.Is(err, ErrInvalidUsageImportSnapshot) {
		t.Fatalf("expected ErrInvalidUsageImportSnapshot, got %v", err)
	}
}
