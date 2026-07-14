package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
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

func TestUsageServiceExportUsageSnapshotStreamsMultipleGroups(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-service-export-stream.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	baseTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{EventKey: "event-a-1", APIGroupKey: "provider-a", Model: "model-a", Timestamp: baseTime, TotalTokens: 10},
		{EventKey: "event-a-2", APIGroupKey: "provider-a", Model: "model-a", Timestamp: baseTime.Add(time.Second), TotalTokens: 20},
		{EventKey: "event-b-1", APIGroupKey: "provider-a", Model: "model-b", Timestamp: baseTime, Failed: true, TotalTokens: 30},
		{EventKey: "event-c-1", APIGroupKey: "provider-b", Model: "model-c", Timestamp: baseTime, TotalTokens: 40},
	}
	if _, _, err = repository.InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	service := &usageService{db: db}
	var output bytes.Buffer
	if err = service.ExportUsageSnapshot(context.Background(), &output, baseTime, servicedto.UsageFilter{}); err != nil {
		t.Fatalf("ExportUsageSnapshot returned error: %v", err)
	}
	var payload struct {
		Version int                        `json:"version"`
		Usage   repodto.StatisticsSnapshot `json:"usage"`
	}
	if err = json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v; body=%s", err, output.String())
	}
	if payload.Version != 3 || payload.Usage.TotalRequests != 4 || payload.Usage.FailureCount != 1 {
		t.Fatalf("unexpected export summary: %+v", payload)
	}
	if details := payload.Usage.APIs["provider-a"].Models["model-a"].Details; len(details) != 2 {
		t.Fatalf("provider-a/model-a details = %d, want 2", len(details))
	}
	if details := payload.Usage.APIs["provider-a"].Models["model-b"].Details; len(details) != 1 {
		t.Fatalf("provider-a/model-b details = %d, want 1", len(details))
	}
	if details := payload.Usage.APIs["provider-b"].Models["model-c"].Details; len(details) != 1 {
		t.Fatalf("provider-b/model-c details = %d, want 1", len(details))
	}
}

func TestUsageServiceImportUsageSnapshotStreamRollsBackFlushedBatches(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-service-import-stream-rollback.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	details := make([]repodto.RequestDetail, usageImportBatchSize+1)
	baseTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < usageImportBatchSize; i++ {
		details[i] = repodto.RequestDetail{
			Timestamp: baseTime.Add(time.Duration(i) * time.Second),
			Tokens:    repodto.TokenStats{TotalTokens: 1},
		}
	}
	payload := struct {
		Version int                        `json:"version"`
		Usage   repodto.StatisticsSnapshot `json:"usage"`
	}{
		Version: 2,
		Usage: repodto.StatisticsSnapshot{APIs: map[string]repodto.APISnapshot{
			"provider-a": {Models: map[string]repodto.ModelSnapshot{
				"model-a": {TotalRequests: int64(len(details)), Details: details},
			}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	service := &usageService{db: db}
	_, err = service.ImportUsageSnapshotStream(context.Background(), bytes.NewReader(body))
	if !errors.Is(err, ErrInvalidUsageImportSnapshot) {
		t.Fatalf("expected ErrInvalidUsageImportSnapshot, got %v", err)
	}
	var count int64
	if err = db.Model(&entities.UsageEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected transaction rollback after invalid trailing detail, got %d rows", count)
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

func TestUsageServiceExportImportPreservesArchivedAggregates(t *testing.T) {
	sourceDB, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-archive-source.db")})
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	closeTestDatabase(t, sourceDB)
	now := time.Date(2026, 7, 14, 12, 34, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{EventKey: "archived-a", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-40 * 24 * time.Hour), TotalTokens: 10},
		{EventKey: "archived-b", APIGroupKey: "provider-a", Model: "model-a", Timestamp: now.Add(-40*24*time.Hour + time.Minute), Failed: true, TotalTokens: 20},
		{EventKey: "recent", APIGroupKey: "provider-b", Model: "model-b", Timestamp: now.Add(-time.Hour), TotalTokens: 30},
	}
	if _, _, err = repository.InsertUsageEvents(sourceDB, events); err != nil {
		t.Fatalf("insert source events: %v", err)
	}
	if _, err = repository.CleanupUsageEvents(sourceDB, now); err != nil {
		t.Fatalf("archive source events: %v", err)
	}

	var output bytes.Buffer
	sourceService := &usageService{db: sourceDB}
	if err = sourceService.ExportUsageSnapshot(context.Background(), &output, now, servicedto.UsageFilter{}); err != nil {
		t.Fatalf("export archived usage: %v", err)
	}
	var exported struct {
		Version    int                        `json:"version"`
		Usage      repodto.StatisticsSnapshot `json:"usage"`
		Aggregates []usageArchiveRecord       `json:"aggregates"`
	}
	if err = json.Unmarshal(output.Bytes(), &exported); err != nil {
		t.Fatalf("decode archived usage export: %v", err)
	}
	if exported.Version != 3 || len(exported.Aggregates) != 1 || exported.Aggregates[0].RequestCount != 2 {
		t.Fatalf("unexpected archived export: %+v", exported)
	}
	archivedModel, ok := exported.Usage.APIs["provider-a"].Models["model-a"]
	if !ok || archivedModel.TotalRequests != 2 || len(archivedModel.Details) != 0 {
		t.Fatalf("expected archive-only model in usage tree, got %+v", exported.Usage.APIs)
	}

	targetDB, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-archive-target.db")})
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	closeTestDatabase(t, targetDB)
	targetService := &usageService{db: targetDB}
	result, err := targetService.ImportUsageSnapshotStream(context.Background(), bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("import archived usage: %v", err)
	}
	if result.Added != 3 || result.Skipped != 0 || result.TotalRequests != 3 || result.FailedCount != 1 {
		t.Fatalf("unexpected archived import result: %+v", result)
	}
	snapshot, err := repository.BuildUsageAggregateSnapshotWithFilter(targetDB, repodto.UsageQueryFilter{})
	if err != nil {
		t.Fatalf("build target snapshot: %v", err)
	}
	if snapshot.TotalRequests != 3 || snapshot.TotalTokens != 60 || snapshot.FailureCount != 1 {
		t.Fatalf("unexpected target snapshot: %+v", snapshot)
	}
	result, err = targetService.ImportUsageSnapshotStream(context.Background(), bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("reimport archived usage: %v", err)
	}
	if result.Added != 0 || result.Skipped != 3 || result.TotalRequests != 3 {
		t.Fatalf("expected archived import deduplication, got %+v", result)
	}
}

func TestUsageServiceImportUsageSnapshotStreamRejectsDuplicateAggregates(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-duplicate-aggregate.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	closeTestDatabase(t, db)
	bucketStart := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	record := usageArchiveRecord{
		BucketStart: bucketStart, APIGroupKey: "provider-a", Model: "model-a",
		RequestCount: 1, SuccessCount: 1, FirstEventAt: bucketStart, LastEventAt: bucketStart,
	}
	payload := struct {
		Version    int                        `json:"version"`
		Usage      repodto.StatisticsSnapshot `json:"usage"`
		Aggregates []usageArchiveRecord       `json:"aggregates"`
	}{
		Version: 3,
		Usage: repodto.StatisticsSnapshot{APIs: map[string]repodto.APISnapshot{
			"provider-a": {Models: map[string]repodto.ModelSnapshot{
				"model-a": {TotalRequests: 1, SuccessCount: 1, Details: []repodto.RequestDetail{}},
			}},
		}},
		Aggregates: []usageArchiveRecord{record, record},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	service := &usageService{db: db}
	_, err = service.ImportUsageSnapshotStream(context.Background(), bytes.NewReader(body))
	if !errors.Is(err, ErrInvalidUsageImportSnapshot) {
		t.Fatalf("expected ErrInvalidUsageImportSnapshot, got %v", err)
	}
	var count int64
	if err = db.Model(&entities.UsageHourlyAggregate{}).Count(&count).Error; err != nil {
		t.Fatalf("count aggregates: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected duplicate aggregate import rollback, got %d rows", count)
	}
}

func TestUsageServiceImportUsageSnapshotStreamRejectsAggregateOverlappingLiveEvent(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-overlapping-aggregate.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	closeTestDatabase(t, db)
	bucketStart := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if _, _, err = repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "live-event", APIGroupKey: "provider-a", Provider: "claude", AuthType: "oauth",
		Model: "model-a", Source: "source-a", AuthIndex: "auth-a", Timestamp: bucketStart.Add(time.Minute),
	}}); err != nil {
		t.Fatalf("insert live event: %v", err)
	}
	record := usageArchiveRecord{
		BucketStart: bucketStart, APIGroupKey: "provider-a", Provider: "claude", AuthType: "oauth",
		Model: "model-a", Source: "source-a", AuthIndex: "auth-a", RequestCount: 1, SuccessCount: 1,
		FirstEventAt: bucketStart, LastEventAt: bucketStart,
	}
	payload := struct {
		Version    int                        `json:"version"`
		Usage      repodto.StatisticsSnapshot `json:"usage"`
		Aggregates []usageArchiveRecord       `json:"aggregates"`
	}{
		Version: 3,
		Usage: repodto.StatisticsSnapshot{APIs: map[string]repodto.APISnapshot{
			"provider-a": {Models: map[string]repodto.ModelSnapshot{
				"model-a": {TotalRequests: 1, SuccessCount: 1, Details: []repodto.RequestDetail{}},
			}},
		}},
		Aggregates: []usageArchiveRecord{record},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	service := &usageService{db: db}
	_, err = service.ImportUsageSnapshotStream(context.Background(), bytes.NewReader(body))
	if !errors.Is(err, ErrInvalidUsageImportSnapshot) {
		t.Fatalf("expected ErrInvalidUsageImportSnapshot, got %v", err)
	}
	var aggregateCount int64
	if err = db.Model(&entities.UsageHourlyAggregate{}).Count(&aggregateCount).Error; err != nil {
		t.Fatalf("count aggregates: %v", err)
	}
	if aggregateCount != 0 {
		t.Fatalf("expected overlapping aggregate import rollback, got %d rows", aggregateCount)
	}
}
