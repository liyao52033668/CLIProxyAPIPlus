package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type usageQueryCaptureLogger struct {
	statements []string
}

func (logger *usageQueryCaptureLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return logger
}

func (*usageQueryCaptureLogger) Info(context.Context, string, ...interface{})  {}
func (*usageQueryCaptureLogger) Warn(context.Context, string, ...interface{})  {}
func (*usageQueryCaptureLogger) Error(context.Context, string, ...interface{}) {}

func (logger *usageQueryCaptureLogger) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	statement, _ := query()
	logger.statements = append(logger.statements, statement)
}

func TestListUsageEventsWithFilterAppliesTimeBoundsAndPagination(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	events := []entities.UsageEvent{
		{EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Source: "source-a", AuthIndex: "1", TotalTokens: 10},
		{EventKey: "event-2", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC), Source: "source-b", AuthIndex: "2", TotalTokens: 20},
		{EventKey: "event-3", APIGroupKey: "provider-b", Model: "claude-opus", Timestamp: time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC), Source: "source-c", AuthIndex: "3", TotalTokens: 30},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	start := time.Date(2026, 4, 16, 9, 30, 0, 0, time.UTC)
	end := time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC)
	page, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{StartTime: &start, EndTime: &end, Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if page.TotalCount != 2 || page.TotalPages != 2 || page.Page != 1 || page.PageSize != 1 {
		t.Fatalf("unexpected pagination metadata: %+v", page)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected one row after page size, got %d", len(page.Events))
	}
	if page.Events[0].Source != "source-c" {
		t.Fatalf("expected newest in-range row first, got %+v", page.Events[0])
	}
	if len(page.Models) != 2 || page.Models[0] != "claude-opus" || page.Models[1] != "claude-sonnet" {
		t.Fatalf("unexpected model options: %+v", page.Models)
	}
}

func TestListUsageEventsWithFilterIncludesOffsetTimestampWithinUTCWindow(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-offset.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	location := time.FixedZone("UTC+8", 8*60*60)
	timestamp := time.Date(2026, 7, 14, 20, 25, 0, 0, location)
	if _, _, err := InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "event-offset", Model: "qmodel-latest", Timestamp: timestamp, Source: "qoder", TotalTokens: 10,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	start := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 14, 15, 59, 59, 999999999, time.UTC)
	page, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{StartTime: &start, EndTime: &end, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if page.TotalCount != 1 || len(page.Events) != 1 {
		t.Fatalf("expected offset timestamp within UTC window, got %+v", page)
	}
	if !page.Events[0].Timestamp.Equal(timestamp) {
		t.Fatalf("timestamp = %s, want %s", page.Events[0].Timestamp, timestamp)
	}
}

func TestListUsageEventsWithFilterSelectsOnlyResponseColumns(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-select.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	if _, _, err := InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet",
		Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Source: "source-a",
		AuthIndex: "1", Provider: "provider", AuthType: "oauth", LatencyMS: 25, TotalTokens: 10,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	capture := &usageQueryCaptureLogger{}
	queryDB := db.Session(&gorm.Session{Logger: capture})
	if _, err := ListUsageEventsWithFilter(queryDB, dto.UsageQueryFilter{Page: 1, PageSize: 20}); err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}

	foundPageQuery := false
	foundModelQuery := false
	for _, statement := range capture.statements {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "distinct trim(model)") {
			foundModelQuery = true
			continue
		}
		if !strings.Contains(lower, "order by timestamp desc, id desc") {
			continue
		}
		foundPageQuery = true
		for _, excluded := range []string{"event_key", "endpoint", "request_id", "model_alias", "created_at"} {
			if strings.Contains(lower, excluded) {
				t.Fatalf("event page query selected unused column %q: %s", excluded, statement)
			}
		}
		for _, required := range []string{"latency_ms", "input_tokens", "output_tokens", "total_tokens"} {
			if !strings.Contains(lower, required) {
				t.Fatalf("event page query omitted required column %q: %s", required, statement)
			}
		}
	}
	if !foundPageQuery || !foundModelQuery {
		t.Fatalf("expected captured event page and model queries, got %+v", capture.statements)
	}
}

func TestListUsageEventsWithFilterPagesByTimestampAndID(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-pages.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	timestamp := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: timestamp, Source: "source-a", AuthIndex: "1", TotalTokens: 10},
		{EventKey: "event-2", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: timestamp, Source: "source-b", AuthIndex: "2", TotalTokens: 20},
		{EventKey: "event-3", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: timestamp.Add(-time.Hour), Source: "source-c", AuthIndex: "3", TotalTokens: 30},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	firstPage, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	secondPage, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if firstPage.TotalCount != 3 || firstPage.TotalPages != 3 || secondPage.TotalCount != 3 || secondPage.TotalPages != 3 {
		t.Fatalf("unexpected page metadata: first=%+v second=%+v", firstPage, secondPage)
	}
	if len(firstPage.Events) != 1 || len(secondPage.Events) != 1 {
		t.Fatalf("expected one event on each page: first=%+v second=%+v", firstPage, secondPage)
	}
	if firstPage.Events[0].ID <= secondPage.Events[0].ID {
		t.Fatalf("expected id desc tie-breaker, first=%+v second=%+v", firstPage.Events[0], secondPage.Events[0])
	}
}

func TestListUsageEventsWithFilterAppliesModelSourceAndResultFilters(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-filtered.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	events := []entities.UsageEvent{
		{EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Source: "source-a", Failed: false, TotalTokens: 10},
		{EventKey: "event-2", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC), Source: "source-a", Failed: true, TotalTokens: 20},
		{EventKey: "event-3", APIGroupKey: "provider-b", Model: "claude-opus", Timestamp: time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC), Source: "source-a", Failed: false, TotalTokens: 30},
		{EventKey: "event-4", APIGroupKey: "provider-c", Model: "gpt-5", Timestamp: time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC), Source: "source-b", Failed: false, TotalTokens: 40},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	page, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{Page: 1, PageSize: 20, Model: "claude-sonnet", Source: "source-a", Result: "success"})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if page.TotalCount != 1 || len(page.Events) != 1 {
		t.Fatalf("expected one matching event, got %+v", page)
	}
	if page.Events[0].Model != "claude-sonnet" || page.Events[0].Source != "source-a" || page.Events[0].Failed {
		t.Fatalf("unexpected filtered event: %+v", page.Events[0])
	}
}

func TestListUsageEventsWithFilterAppliesAuthIndexFilter(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-auth-filter.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	events := []entities.UsageEvent{
		{EventKey: "event-1", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Source: "auth-1", AuthIndex: "auth-1", TotalTokens: 10},
		{EventKey: "event-2", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC), Source: "source-alias", AuthIndex: "auth-1", TotalTokens: 20},
		{EventKey: "event-3", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC), Source: "other", AuthIndex: "other", TotalTokens: 30},
		{EventKey: "event-4", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC), Source: "auth-1", AuthIndex: "auth-1", Provider: "Provider A", TotalTokens: 40},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	page, err := ListUsageEventsWithFilter(db, dto.UsageQueryFilter{Source: "auth-1", AuthIndex: "auth-1", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter returned error: %v", err)
	}
	if page.TotalCount != 3 || len(page.Events) != 3 {
		t.Fatalf("expected three matching auth events, got %+v", page)
	}
	for _, event := range page.Events {
		if event.AuthIndex != "auth-1" {
			t.Fatalf("unexpected auth filtered event: %+v", event)
		}
	}
}

func TestListUsageEventFilterOptionsWithFilterReturnsStableModels(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-events-filter-options.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	events := []entities.UsageEvent{
		{EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Source: "source-a", Failed: false, TotalTokens: 10},
		{EventKey: "event-2", APIGroupKey: "provider-a", Model: "claude-sonnet", Timestamp: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC), Source: "source-b", Failed: true, TotalTokens: 20},
		{EventKey: "event-3", APIGroupKey: "provider-b", Model: "gpt-5", Timestamp: time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC), Source: "source-a", Failed: false, TotalTokens: 30},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	options, err := ListUsageEventFilterOptionsWithFilter(db, dto.UsageQueryFilter{Result: "success"})
	if err != nil {
		t.Fatalf("ListUsageEventFilterOptionsWithFilter returned error: %v", err)
	}
	if len(options.Models) != 2 || options.Models[0] != "claude-sonnet" || options.Models[1] != "gpt-5" {
		t.Fatalf("expected stable model options, got %+v", options.Models)
	}
}

func TestListUsageAnalysisWithFilterAggregatesApisAndModels(t *testing.T) {
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "usage-analysis.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)

	events := []entities.UsageEvent{
		{
			EventKey: "event-1", APIGroupKey: "provider-a", Model: "claude-sonnet",
			Timestamp: time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC), Failed: false, LatencyMS: 100,
			InputTokens: 10, OutputTokens: 4, ReasoningTokens: 2, CachedTokens: 1, TotalTokens: 17,
		},
		{
			EventKey: "event-2", APIGroupKey: "provider-a", Model: "claude-sonnet",
			Timestamp: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC), Failed: true, LatencyMS: 250,
			InputTokens: 20, OutputTokens: 5, ReasoningTokens: 0, CachedTokens: 0, TotalTokens: 25,
		},
		{
			EventKey: "event-3", APIGroupKey: "provider-b", Model: "gpt-5",
			Timestamp: time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC), Failed: false, LatencyMS: 400,
			InputTokens: 30, OutputTokens: 7, ReasoningTokens: 3, CachedTokens: 2, TotalTokens: 42,
		},
	}
	if _, _, err := InsertUsageEvents(db, events); err != nil {
		t.Fatalf("InsertUsageEvents returned error: %v", err)
	}

	start := time.Date(2026, 4, 16, 9, 30, 0, 0, time.UTC)
	end := time.Date(2026, 4, 16, 11, 30, 0, 0, time.UTC)
	capture := &usageQueryCaptureLogger{}
	queryDB := db.Session(&gorm.Session{Logger: capture})
	apiRows, modelRows, err := ListUsageAnalysisWithFilter(queryDB, dto.UsageQueryFilter{StartTime: &start, EndTime: &end})
	if err != nil {
		t.Fatalf("ListUsageAnalysisWithFilter returned error: %v", err)
	}
	aggregateQueries := 0
	for _, statement := range capture.statements {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "usage_events") && strings.Contains(lower, "group by") {
			aggregateQueries++
		}
	}
	if aggregateQueries != 1 {
		t.Fatalf("usage analysis aggregate queries = %d, want 1; statements=%+v", aggregateQueries, capture.statements)
	}
	if len(apiRows) != 2 {
		t.Fatalf("expected two api rows, got %d", len(apiRows))
	}
	if len(modelRows) != 2 {
		t.Fatalf("expected two model rows, got %d", len(modelRows))
	}
	if apiRows[0].APIGroupKey != "provider-a" || apiRows[0].TotalRequests != 1 || apiRows[0].FailureCount != 1 || apiRows[0].TotalTokens != 25 {
		t.Fatalf("unexpected first api row: %+v", apiRows[0])
	}
	modelByName := map[string]dto.UsageAnalysisModelStatRecord{}
	for _, row := range modelRows {
		modelByName[row.Model] = row
	}
	if row := modelByName["gpt-5"]; row.Model != "gpt-5" || row.TotalRequests != 1 || row.TotalLatencyMS != 400 || row.LatencySampleCount != 1 {
		t.Fatalf("unexpected gpt-5 model row: %+v", row)
	}
	if row := modelByName["claude-sonnet"]; row.Model != "claude-sonnet" || row.FailureCount != 1 || row.InputTokens != 20 || row.CachedTokens != 0 {
		t.Fatalf("unexpected claude-sonnet model row: %+v", row)
	}
}
