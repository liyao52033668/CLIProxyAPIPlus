package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type usageService struct {
	db *gorm.DB
}

type usageArchiveRecord struct {
	BucketStart        time.Time `json:"bucket_start"`
	APIGroupKey        string    `json:"api_group_key"`
	Provider           string    `json:"provider"`
	AuthType           string    `json:"auth_type"`
	Model              string    `json:"model"`
	Source             string    `json:"source"`
	AuthIndex          string    `json:"auth_index"`
	RequestCount       int64     `json:"request_count"`
	SuccessCount       int64     `json:"success_count"`
	FailureCount       int64     `json:"failure_count"`
	InputTokens        int64     `json:"input_tokens"`
	OutputTokens       int64     `json:"output_tokens"`
	ReasoningTokens    int64     `json:"reasoning_tokens"`
	CachedTokens       int64     `json:"cached_tokens"`
	TotalTokens        int64     `json:"total_tokens"`
	TotalLatencyMS     int64     `json:"total_latency_ms"`
	LatencySampleCount int64     `json:"latency_sample_count"`
	FirstEventAt       time.Time `json:"first_event_at"`
	LastEventAt        time.Time `json:"last_event_at"`
}

func usageArchiveRecordFromEntity(aggregate entities.UsageHourlyAggregate) usageArchiveRecord {
	return usageArchiveRecord{
		BucketStart: aggregate.BucketStart, APIGroupKey: aggregate.APIGroupKey, Provider: aggregate.Provider,
		AuthType: aggregate.AuthType, Model: aggregate.Model, Source: aggregate.Source, AuthIndex: aggregate.AuthIndex,
		RequestCount: aggregate.RequestCount, SuccessCount: aggregate.SuccessCount, FailureCount: aggregate.FailureCount,
		InputTokens: aggregate.InputTokens, OutputTokens: aggregate.OutputTokens, ReasoningTokens: aggregate.ReasoningTokens,
		CachedTokens: aggregate.CachedTokens, TotalTokens: aggregate.TotalTokens, TotalLatencyMS: aggregate.TotalLatencyMS,
		LatencySampleCount: aggregate.LatencySampleCount, FirstEventAt: aggregate.FirstEventAt, LastEventAt: aggregate.LastEventAt,
	}
}

func (record usageArchiveRecord) entity() (entities.UsageHourlyAggregate, error) {
	if record.BucketStart.IsZero() || record.RequestCount <= 0 || record.SuccessCount < 0 || record.FailureCount < 0 ||
		record.SuccessCount+record.FailureCount != record.RequestCount {
		return entities.UsageHourlyAggregate{}, fmt.Errorf("%w: invalid usage aggregate", ErrInvalidUsageImportSnapshot)
	}
	firstEventAt := record.FirstEventAt
	if firstEventAt.IsZero() {
		firstEventAt = record.BucketStart
	}
	lastEventAt := record.LastEventAt
	if lastEventAt.IsZero() {
		lastEventAt = firstEventAt
	}
	return entities.UsageHourlyAggregate{
		BucketStart: record.BucketStart, APIGroupKey: record.APIGroupKey, Provider: record.Provider,
		AuthType: record.AuthType, Model: record.Model, Source: record.Source, AuthIndex: record.AuthIndex,
		RequestCount: record.RequestCount, SuccessCount: record.SuccessCount, FailureCount: record.FailureCount,
		InputTokens: record.InputTokens, OutputTokens: record.OutputTokens, ReasoningTokens: record.ReasoningTokens,
		CachedTokens: record.CachedTokens, TotalTokens: record.TotalTokens, TotalLatencyMS: record.TotalLatencyMS,
		LatencySampleCount: record.LatencySampleCount, FirstEventAt: firstEventAt, LastEventAt: lastEventAt,
	}, nil
}

func NewUsageService(db *gorm.DB) UsageProvider {
	return &usageService{db: db}
}

func (s *usageService) GetUsageWithFilter(_ context.Context, filter servicedto.UsageFilter) (*repodto.StatisticsSnapshot, error) {
	return repository.BuildUsageSnapshotWithFilter(s.db, repodto.UsageQueryFilter{
		Range:     filter.Range,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
}

func (s *usageService) GetUsageAggregateWithFilter(_ context.Context, filter servicedto.UsageFilter) (*repodto.StatisticsSnapshot, error) {
	return repository.BuildUsageAggregateSnapshotWithFilter(s.db, repodto.UsageQueryFilter{
		Range:     filter.Range,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
}

func (s *usageService) GetRecentUsageMinuteBuckets(ctx context.Context, filter servicedto.UsageFilter) (map[string]repodto.UsageBucketSnapshot, error) {
	return repository.BuildRecentUsageMinuteBucketsWithFilter(s.db.WithContext(ctx), repodto.UsageQueryFilter{
		Range:     filter.Range,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
}

// GetUsageOverview builds the aggregate overview for the selected time window.
func (s *usageService) GetUsageOverview(_ context.Context, filter servicedto.UsageFilter) (*servicedto.UsageOverviewSnapshot, error) {
	overview, err := repository.BuildUsageOverviewWithFilter(s.db, repodto.UsageQueryFilter{
		Range:     filter.Range,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
	if err != nil {
		return nil, err
	}
	bucketByDay := filter.Range == "all" || filter.Range == "7d" || overview.Summary.WindowMinutes >= 1440

	return &servicedto.UsageOverviewSnapshot{
		Usage: overview.Usage,
		Summary: servicedto.UsageOverviewSummary{
			RequestCount:    overview.Summary.RequestCount,
			TokenCount:      overview.Summary.TokenCount,
			WindowMinutes:   overview.Summary.WindowMinutes,
			RPM:             overview.Summary.RPM,
			TPM:             overview.Summary.TPM,
			TotalCost:       overview.Summary.TotalCost,
			CostAvailable:   overview.Summary.CostAvailable,
			CachedTokens:    overview.Summary.CachedTokens,
			ReasoningTokens: overview.Summary.ReasoningTokens,
		},
		Series:       mapUsageOverviewSeries(overview.Series),
		HourlySeries: mapUsageOverviewSeries(overview.HourlySeries),
		DailySeries:  mapUsageOverviewSeries(overview.DailySeries),
		Health:       buildUsageOverviewHealth(overview),
		KeyStats:     mapUsageKeyStats(overview.KeyStats),
		StartTime:    filter.StartTime,
		EndTime:      filter.EndTime,
		BucketByDay:  bucketByDay,
	}, nil
}

func mapUsageKeyStats(stats repodto.UsageKeyStatsRecord) servicedto.UsageKeyStats {
	bySource := make(map[string]servicedto.UsageKeyCount, len(stats.BySource))
	for key, count := range stats.BySource {
		bySource[key] = servicedto.UsageKeyCount{
			Success: count.Success,
			Failure: count.Failure,
			Tokens:  count.Tokens,
			Cost:    count.Cost,
		}
	}
	byAuthIndex := make(map[string]servicedto.UsageKeyCount, len(stats.ByAuthIndex))
	for key, count := range stats.ByAuthIndex {
		byAuthIndex[key] = servicedto.UsageKeyCount{
			Success: count.Success,
			Failure: count.Failure,
			Tokens:  count.Tokens,
			Cost:    count.Cost,
		}
	}
	credentials := make([]servicedto.UsageCredentialCount, 0, len(stats.Credentials))
	for key, count := range stats.Credentials {
		credentials = append(credentials, servicedto.UsageCredentialCount{
			Source:    key.Source,
			AuthIndex: key.AuthIndex,
			Success:   count.Success,
			Failure:   count.Failure,
			Tokens:    count.Tokens,
			Cost:      count.Cost,
		})
	}
	sort.Slice(credentials, func(i, j int) bool {
		if credentials[i].AuthIndex == credentials[j].AuthIndex {
			return credentials[i].Source < credentials[j].Source
		}
		return credentials[i].AuthIndex < credentials[j].AuthIndex
	})
	return servicedto.UsageKeyStats{
		BySource:    bySource,
		ByAuthIndex: byAuthIndex,
		Credentials: credentials,
	}
}

func mapUsageOverviewSeries(series repodto.UsageOverviewSeriesRecord) servicedto.UsageOverviewSeries {
	models := make(map[string]servicedto.UsageOverviewSeries, len(series.Models))
	for model, modelSeries := range series.Models {
		models[model] = mapUsageOverviewSeries(modelSeries)
	}
	return servicedto.UsageOverviewSeries{
		Requests:        series.Requests,
		Tokens:          series.Tokens,
		RPM:             series.RPM,
		TPM:             series.TPM,
		Cost:            series.Cost,
		InputTokens:     series.InputTokens,
		OutputTokens:    series.OutputTokens,
		CachedTokens:    series.CachedTokens,
		ReasoningTokens: series.ReasoningTokens,
		Models:          models,
	}
}

func buildUsageOverviewHealth(overview *repodto.UsageOverviewRecord) servicedto.UsageOverviewHealth {
	if overview == nil {
		return servicedto.UsageOverviewHealth{BlockDetails: []servicedto.UsageOverviewHealthBlock{}}
	}
	blocks := make([]servicedto.UsageOverviewHealthBlock, 0, len(overview.Health.BlockDetails))
	for _, block := range overview.Health.BlockDetails {
		blocks = append(blocks, servicedto.UsageOverviewHealthBlock{
			StartTime: block.StartTime,
			EndTime:   block.EndTime,
			Success:   block.Success,
			Failure:   block.Failure,
			Rate:      block.Rate,
		})
	}
	return servicedto.UsageOverviewHealth{
		TotalSuccess:  overview.Health.TotalSuccess,
		TotalFailure:  overview.Health.TotalFailure,
		SuccessRate:   overview.Health.SuccessRate,
		Rows:          overview.Health.Rows,
		Columns:       overview.Health.Columns,
		BucketSeconds: overview.Health.BucketSeconds,
		WindowStart:   overview.Health.WindowStart,
		WindowEnd:     overview.Health.WindowEnd,
		BlockDetails:  blocks,
	}
}

// ListUsageEvents applies pagination and event-list filters.
func (s *usageService) ListUsageEvents(_ context.Context, filter servicedto.UsageFilter) (*servicedto.UsageEventsPage, error) {
	page, err := repository.ListUsageEventsWithFilter(s.db, repodto.UsageQueryFilter{
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
		Limit:     filter.Limit,
		Page:      filter.Page,
		PageSize:  filter.PageSize,
		Offset:    filter.Offset,
		Model:     filter.Model,
		Source:    filter.Source,
		AuthIndex: filter.AuthIndex,
		Result:    filter.Result,
	})
	if err != nil {
		return nil, err
	}
	result := make([]servicedto.UsageEventRecord, 0, len(page.Events))
	for _, row := range page.Events {
		result = append(result, servicedto.UsageEventRecord{
			ID:              row.ID,
			Timestamp:       row.Timestamp,
			APIGroupKey:     row.APIGroupKey,
			Model:           row.Model,
			AuthType:        row.AuthType,
			Provider:        row.Provider,
			Source:          row.Source,
			AuthIndex:       row.AuthIndex,
			Failed:          row.Failed,
			LatencyMS:       row.LatencyMS,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			ReasoningTokens: row.ReasoningTokens,
			CachedTokens:    row.CachedTokens,
			TotalTokens:     row.TotalTokens,
		})
	}
	return &servicedto.UsageEventsPage{Events: result, Models: page.Models, TotalCount: page.TotalCount, Page: page.Page, PageSize: page.PageSize, TotalPages: page.TotalPages}, nil
}

// ListUsageEventFilterOptions loads model candidates for the selected time window.
func (s *usageService) ListUsageEventFilterOptions(_ context.Context, filter servicedto.UsageFilter) (*servicedto.UsageEventFilterOptions, error) {
	options, err := repository.ListUsageEventFilterOptionsWithFilter(s.db, repodto.UsageQueryFilter{
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
	if err != nil {
		return nil, err
	}
	return &servicedto.UsageEventFilterOptions{Models: options.Models}, nil
}

// GetUsageAnalysis returns API and model aggregates for the selected time window.
func (s *usageService) GetUsageAnalysis(_ context.Context, filter servicedto.UsageFilter) (*servicedto.UsageAnalysisSnapshot, error) {
	apiRows, modelRows, err := repository.ListUsageAnalysisWithFilter(s.db, repodto.UsageQueryFilter{
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	})
	if err != nil {
		return nil, err
	}

	apis := make([]servicedto.UsageAnalysisAPIStat, 0, len(apiRows))
	for _, row := range apiRows {
		models := make([]servicedto.UsageAnalysisModelStat, 0, len(row.Models))
		for _, model := range row.Models {
			models = append(models, servicedto.UsageAnalysisModelStat{
				Model:              model.Model,
				TotalRequests:      model.TotalRequests,
				SuccessCount:       model.SuccessCount,
				FailureCount:       model.FailureCount,
				TotalTokens:        model.TotalTokens,
				InputTokens:        model.InputTokens,
				OutputTokens:       model.OutputTokens,
				ReasoningTokens:    model.ReasoningTokens,
				CachedTokens:       model.CachedTokens,
				TotalLatencyMS:     model.TotalLatencyMS,
				LatencySampleCount: model.LatencySampleCount,
			})
		}
		apis = append(apis, servicedto.UsageAnalysisAPIStat{
			APIKey:          row.APIGroupKey,
			DisplayName:     row.DisplayName,
			TotalRequests:   row.TotalRequests,
			SuccessCount:    row.SuccessCount,
			FailureCount:    row.FailureCount,
			TotalTokens:     row.TotalTokens,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			ReasoningTokens: row.ReasoningTokens,
			CachedTokens:    row.CachedTokens,
			Models:          models,
		})
	}

	models := make([]servicedto.UsageAnalysisModelStat, 0, len(modelRows))
	for _, row := range modelRows {
		models = append(models, servicedto.UsageAnalysisModelStat{
			Model:              row.Model,
			TotalRequests:      row.TotalRequests,
			SuccessCount:       row.SuccessCount,
			FailureCount:       row.FailureCount,
			TotalTokens:        row.TotalTokens,
			InputTokens:        row.InputTokens,
			OutputTokens:       row.OutputTokens,
			ReasoningTokens:    row.ReasoningTokens,
			CachedTokens:       row.CachedTokens,
			TotalLatencyMS:     row.TotalLatencyMS,
			LatencySampleCount: row.LatencySampleCount,
		})
	}

	return &servicedto.UsageAnalysisSnapshot{APIs: apis, Models: models}, nil
}

func (s *usageService) ImportUsageSnapshot(_ context.Context, snapshot *repodto.StatisticsSnapshot) (*servicedto.UsageImportResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	if snapshot == nil {
		return nil, fmt.Errorf("%w: usage snapshot is required", ErrInvalidUsageImportSnapshot)
	}

	events, err := buildUsageImportEvents(snapshot)
	if err != nil {
		return nil, err
	}
	inserted, deduped, err := repository.InsertUsageEvents(s.db, events)
	if err != nil {
		return nil, err
	}

	current, err := repository.BuildUsageSnapshot(s.db)
	if err != nil {
		return nil, err
	}
	return &servicedto.UsageImportResult{
		Added:         inserted,
		Skipped:       deduped,
		TotalRequests: current.TotalRequests,
		FailedCount:   current.FailureCount,
	}, nil
}

func buildUsageImportEvents(snapshot *repodto.StatisticsSnapshot) ([]entities.UsageEvent, error) {
	events := make([]entities.UsageEvent, 0)
	hasDetails := false

	for apiGroupKey, apiStats := range snapshot.APIs {
		for modelName, modelStats := range apiStats.Models {
			if modelStats.TotalRequests > 0 && len(modelStats.Details) == 0 {
				return nil, fmt.Errorf("%w: model %q for api %q does not contain request details", ErrInvalidUsageImportSnapshot, strings.TrimSpace(modelName), strings.TrimSpace(apiGroupKey))
			}
			for _, detail := range modelStats.Details {
				hasDetails = true
				event, err := usageEventFromImportDetail(apiGroupKey, modelName, detail)
				if err != nil {
					return nil, err
				}
				events = append(events, event)
			}
		}
	}

	if !hasDetails {
		return nil, fmt.Errorf("%w: usage snapshot does not contain request details", ErrInvalidUsageImportSnapshot)
	}
	return events, nil
}

const usageImportBatchSize = 500

func (s *usageService) ExportUsageSnapshot(ctx context.Context, output io.Writer, exportedAt time.Time, filter servicedto.UsageFilter) error {
	if s.db == nil {
		return fmt.Errorf("database is nil")
	}
	if output == nil {
		return fmt.Errorf("usage export writer is nil")
	}

	queryFilter := repodto.UsageQueryFilter{
		Range:     filter.Range,
		StartTime: filter.StartTime,
		EndTime:   filter.EndTime,
	}
	snapshot, err := repository.BuildUsageAggregateSnapshotWithFilter(s.db.WithContext(ctx), queryFilter)
	if err != nil {
		return err
	}
	writer := &usageJSONStreamWriter{output: output}
	writer.write(`{"version":3,"exported_at":`)
	writer.writeJSON(exportedAt.UTC())
	writer.write(`,"usage":{"total_requests":`)
	writer.writeInt(snapshot.TotalRequests)
	writer.write(`,"success_count":`)
	writer.writeInt(snapshot.SuccessCount)
	writer.write(`,"failure_count":`)
	writer.writeInt(snapshot.FailureCount)
	writer.write(`,"total_tokens":`)
	writer.writeInt(snapshot.TotalTokens)
	writer.write(`,"apis":{`)
	if writer.err != nil {
		return writer.err
	}

	var currentAPI string
	var currentModel string
	hasAPI := false
	hasModel := false
	firstAPI := true
	firstModel := true
	firstDetail := true
	emittedAPIs := make(map[string]struct{}, len(snapshot.APIs))
	emittedModels := make(map[string]struct{})
	closeModel := func() {
		if hasModel {
			writer.write(`]}`)
			hasModel = false
		}
	}
	writeModel := func(apiKey, modelName string, withDetails bool) error {
		modelStats, ok := snapshot.APIs[apiKey].Models[modelName]
		if !ok {
			return fmt.Errorf("usage export aggregate missing model %q for api %q", modelName, apiKey)
		}
		if !firstModel {
			writer.write(`,`)
		}
		firstModel = false
		currentModel = modelName
		emittedModels[modelName] = struct{}{}
		writer.writeJSON(modelName)
		writer.write(`:{"total_requests":`)
		writer.writeInt(modelStats.TotalRequests)
		writer.write(`,"success_count":`)
		writer.writeInt(modelStats.SuccessCount)
		writer.write(`,"failure_count":`)
		writer.writeInt(modelStats.FailureCount)
		writer.write(`,"total_tokens":`)
		writer.writeInt(modelStats.TotalTokens)
		writer.write(`,"details":[`)
		if withDetails {
			hasModel = true
			firstDetail = true
		} else {
			writer.write(`]}`)
		}
		return writer.err
	}
	openAPI := func(apiKey string) error {
		apiStats, ok := snapshot.APIs[apiKey]
		if !ok {
			return fmt.Errorf("usage export aggregate missing api %q", apiKey)
		}
		if !firstAPI {
			writer.write(`,`)
		}
		firstAPI = false
		currentAPI = apiKey
		currentModel = ""
		firstModel = true
		emittedModels = make(map[string]struct{}, len(apiStats.Models))
		writer.writeJSON(apiKey)
		writer.write(`:{`)
		if apiStats.DisplayName != "" {
			writer.write(`"display_name":`)
			writer.writeJSON(apiStats.DisplayName)
			writer.write(`,`)
		}
		writer.write(`"total_requests":`)
		writer.writeInt(apiStats.TotalRequests)
		writer.write(`,"success_count":`)
		writer.writeInt(apiStats.SuccessCount)
		writer.write(`,"failure_count":`)
		writer.writeInt(apiStats.FailureCount)
		writer.write(`,"total_tokens":`)
		writer.writeInt(apiStats.TotalTokens)
		writer.write(`,"models":{`)
		hasAPI = true
		return writer.err
	}
	closeAPI := func() error {
		if !hasAPI {
			return writer.err
		}
		closeModel()
		modelNames := make([]string, 0, len(snapshot.APIs[currentAPI].Models))
		for modelName := range snapshot.APIs[currentAPI].Models {
			modelNames = append(modelNames, modelName)
		}
		sort.Strings(modelNames)
		for _, modelName := range modelNames {
			if _, emitted := emittedModels[modelName]; emitted {
				continue
			}
			if errWrite := writeModel(currentAPI, modelName, false); errWrite != nil {
				return errWrite
			}
		}
		writer.write(`}}`)
		emittedAPIs[currentAPI] = struct{}{}
		hasAPI = false
		return writer.err
	}

	err = repository.StreamUsageEventsForExport(ctx, s.db, queryFilter, func(event entities.UsageEvent) error {
		apiKey := usageSnapshotDimension(event.APIGroupKey)
		modelName := usageSnapshotDimension(event.Model)
		if !hasAPI || apiKey != currentAPI {
			if errClose := closeAPI(); errClose != nil {
				return errClose
			}
			if errOpen := openAPI(apiKey); errOpen != nil {
				return errOpen
			}
		}
		if !hasModel || modelName != currentModel {
			closeModel()
			if errWrite := writeModel(apiKey, modelName, true); errWrite != nil {
				return errWrite
			}
		}
		if !firstDetail {
			writer.write(`,`)
		}
		firstDetail = false
		writer.writeJSON(repodto.RequestDetail{
			Timestamp: event.Timestamp.UTC(),
			LatencyMS: event.LatencyMS,
			Source:    strings.TrimSpace(event.Source),
			AuthIndex: strings.TrimSpace(event.AuthIndex),
			Failed:    event.Failed,
			Tokens: repodto.TokenStats{
				InputTokens:     event.InputTokens,
				OutputTokens:    event.OutputTokens,
				ReasoningTokens: event.ReasoningTokens,
				CachedTokens:    event.CachedTokens,
				TotalTokens:     event.TotalTokens,
			},
		})
		return writer.err
	})
	if err != nil {
		return err
	}
	if err = closeAPI(); err != nil {
		return err
	}
	apiKeys := make([]string, 0, len(snapshot.APIs))
	for apiKey := range snapshot.APIs {
		apiKeys = append(apiKeys, apiKey)
	}
	sort.Strings(apiKeys)
	for _, apiKey := range apiKeys {
		if _, emitted := emittedAPIs[apiKey]; emitted {
			continue
		}
		if err = openAPI(apiKey); err != nil {
			return err
		}
		if err = closeAPI(); err != nil {
			return err
		}
	}
	writer.write(`},"requests_by_day":`)
	writer.writeJSON(snapshot.RequestsByDay)
	writer.write(`,"requests_by_hour":`)
	writer.writeJSON(snapshot.RequestsByHour)
	writer.write(`,"tokens_by_day":`)
	writer.writeJSON(snapshot.TokensByDay)
	writer.write(`,"tokens_by_hour":`)
	writer.writeJSON(snapshot.TokensByHour)
	writer.write(`},"aggregates":[`)
	firstAggregate := true
	err = repository.StreamUsageHourlyAggregatesForExport(ctx, s.db, queryFilter, func(aggregate entities.UsageHourlyAggregate) error {
		if !firstAggregate {
			writer.write(`,`)
		}
		firstAggregate = false
		writer.writeJSON(usageArchiveRecordFromEntity(aggregate))
		return writer.err
	})
	if err != nil {
		return err
	}
	writer.write(`]}`)
	return writer.err
}

func (s *usageService) ImportUsageSnapshotStream(ctx context.Context, input io.Reader) (*servicedto.UsageImportResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	if input == nil {
		return nil, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}

	inputFile, err := os.CreateTemp("", "cli-proxy-usage-import-*.json")
	if err != nil {
		return nil, fmt.Errorf("create usage import file: %w", err)
	}
	inputPath := inputFile.Name()
	defer func() {
		if errClose := inputFile.Close(); errClose != nil {
			logrus.WithError(errClose).Error("failed to close usage import file")
		}
		if errRemove := os.Remove(inputPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			logrus.WithError(errRemove).Error("failed to remove usage import file")
		}
	}()
	if _, err = io.Copy(inputFile, input); err != nil {
		return nil, fmt.Errorf("store usage import: %w", err)
	}
	if _, err = inputFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind usage import: %w", err)
	}

	var result servicedto.UsageImportResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accumulator := newUsageImportAccumulator(tx)
		if errDecode := decodeUsageImportStream(json.NewDecoder(inputFile), &accumulator); errDecode != nil {
			return errDecode
		}
		if errFlush := accumulator.flush(); errFlush != nil {
			return errFlush
		}
		if !accumulator.hasData {
			return fmt.Errorf("%w: usage snapshot does not contain request details or aggregates", ErrInvalidUsageImportSnapshot)
		}
		result.Added = accumulator.added
		result.Skipped = accumulator.skipped
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.TotalRequests, result.FailedCount, err = repository.GetUsageRequestTotals(s.db.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("load imported usage totals: %w", err)
	}
	return &result, nil
}

type usageJSONStreamWriter struct {
	output io.Writer
	err    error
}

func (w *usageJSONStreamWriter) write(value string) {
	if w.err != nil {
		return
	}
	_, w.err = io.WriteString(w.output, value)
}

func (w *usageJSONStreamWriter) writeInt(value int64) {
	w.write(strconv.FormatInt(value, 10))
}

func (w *usageJSONStreamWriter) writeJSON(value any) {
	if w.err != nil {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		w.err = err
		return
	}
	_, w.err = w.output.Write(encoded)
}

type usageModelKey struct {
	apiGroupKey string
	model       string
}

type usageImportAccumulator struct {
	db                   *gorm.DB
	events               []entities.UsageEvent
	aggregates           []entities.UsageHourlyAggregate
	aggregateKeys        map[string]struct{}
	aggregateModels      map[usageModelKey]struct{}
	modelsWithoutDetails map[usageModelKey]struct{}
	added                int
	skipped              int
	hasData              bool
}

func newUsageImportAccumulator(db *gorm.DB) usageImportAccumulator {
	return usageImportAccumulator{
		db:                   db,
		events:               make([]entities.UsageEvent, 0, usageImportBatchSize),
		aggregates:           make([]entities.UsageHourlyAggregate, 0, usageImportBatchSize),
		aggregateKeys:        make(map[string]struct{}),
		aggregateModels:      make(map[usageModelKey]struct{}),
		modelsWithoutDetails: make(map[usageModelKey]struct{}),
	}
}

func (a *usageImportAccumulator) append(apiGroupKey, modelName string, detail repodto.RequestDetail) error {
	event, err := usageEventFromImportDetail(apiGroupKey, modelName, detail)
	if err != nil {
		return err
	}
	a.hasData = true
	a.events = append(a.events, event)
	if len(a.events) >= usageImportBatchSize {
		return a.flush()
	}
	return nil
}

func (a *usageImportAccumulator) appendAggregate(record usageArchiveRecord) error {
	aggregate, err := record.entity()
	if err != nil {
		return err
	}
	aggregate = repository.NormalizeUsageHourlyAggregate(aggregate)
	if _, exists := a.aggregateKeys[aggregate.AggregateKey]; exists {
		return fmt.Errorf("%w: duplicate usage aggregate", ErrInvalidUsageImportSnapshot)
	}
	a.aggregateKeys[aggregate.AggregateKey] = struct{}{}
	a.aggregateModels[usageModelKey{
		apiGroupKey: usageSnapshotDimension(aggregate.APIGroupKey),
		model:       usageSnapshotDimension(aggregate.Model),
	}] = struct{}{}
	a.hasData = true
	a.aggregates = append(a.aggregates, aggregate)
	if len(a.aggregates) >= usageImportBatchSize {
		return a.flush()
	}
	return nil
}

func (a *usageImportAccumulator) flush() error {
	if len(a.events) > 0 {
		inserted, deduped, err := repository.InsertUsageEvents(a.db, a.events)
		if err != nil {
			return fmt.Errorf("insert usage import batch: %w", err)
		}
		a.added += inserted
		a.skipped += deduped
		a.events = a.events[:0]
	}
	if len(a.aggregates) > 0 {
		added, skipped, err := repository.InsertUsageHourlyAggregates(a.db, a.aggregates)
		if err != nil {
			if errors.Is(err, repository.ErrDuplicateUsageAggregate) || errors.Is(err, repository.ErrUsageAggregateOverlapsEvent) {
				return fmt.Errorf("%w: %v", ErrInvalidUsageImportSnapshot, err)
			}
			return fmt.Errorf("insert usage aggregate import batch: %w", err)
		}
		a.added += int(added)
		a.skipped += int(skipped)
		a.aggregates = a.aggregates[:0]
	}
	return nil
}

func decodeUsageImportStream(decoder *json.Decoder, accumulator *usageImportAccumulator) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	if token != json.Delim('{') {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}

	version := 0
	usagePresent := false
	for decoder.More() {
		fieldToken, errField := decoder.Token()
		if errField != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		switch field {
		case "version":
			if errDecode := decoder.Decode(&version); errDecode != nil {
				return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
			}
		case "usage":
			present, errUsage := decodeUsageSnapshotObject(decoder, accumulator)
			if errUsage != nil {
				return errUsage
			}
			usagePresent = present
		case "aggregates":
			if errAggregates := decodeUsageAggregatesArray(decoder, accumulator); errAggregates != nil {
				return errAggregates
			}
		default:
			if errSkip := skipJSONValue(decoder); errSkip != nil {
				return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
			}
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	if version != 0 && version != 1 && version != 2 && version != 3 {
		return fmt.Errorf("%w: %d", ErrUnsupportedUsageImportVersion, version)
	}
	if !usagePresent {
		return fmt.Errorf("%w: usage snapshot is required", ErrInvalidUsageImportSnapshot)
	}
	if errValidate := accumulator.validateModelsWithoutDetails(version); errValidate != nil {
		return errValidate
	}
	return nil
}

func (a *usageImportAccumulator) validateModelsWithoutDetails(version int) error {
	for key := range a.modelsWithoutDetails {
		if version == 3 {
			if _, archived := a.aggregateModels[key]; archived {
				continue
			}
		}
		return fmt.Errorf("%w: model %q for api %q does not contain request details", ErrInvalidUsageImportSnapshot, key.model, key.apiGroupKey)
	}
	return nil
}

func decodeUsageAggregatesArray(decoder *json.Decoder, accumulator *usageImportAccumulator) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return fmt.Errorf("%w: invalid aggregates", ErrInvalidUsageImportJSON)
	}
	for decoder.More() {
		var record usageArchiveRecord
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("%w: invalid aggregate", ErrInvalidUsageImportJSON)
		}
		if err := accumulator.appendAggregate(record); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid aggregates", ErrInvalidUsageImportJSON)
	}
	return nil
}

func decodeUsageSnapshotObject(decoder *json.Decoder, accumulator *usageImportAccumulator) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	if token == nil {
		return false, nil
	}
	if token != json.Delim('{') {
		return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	for decoder.More() {
		fieldToken, errField := decoder.Token()
		if errField != nil {
			return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		if field == "apis" {
			if errAPIs := decodeUsageAPIsObject(decoder, accumulator); errAPIs != nil {
				return false, errAPIs
			}
		} else if errSkip := skipJSONValue(decoder); errSkip != nil {
			return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
	}
	if _, err = decoder.Token(); err != nil {
		return false, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	return true, nil
}

func decodeUsageAPIsObject(decoder *json.Decoder, accumulator *usageImportAccumulator) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	for decoder.More() {
		apiToken, errAPI := decoder.Token()
		if errAPI != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		apiGroupKey, ok := apiToken.(string)
		if !ok {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		if errObject := decodeUsageAPIObject(decoder, accumulator, apiGroupKey); errObject != nil {
			return errObject
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	return nil
}

func decodeUsageAPIObject(decoder *json.Decoder, accumulator *usageImportAccumulator, apiGroupKey string) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	for decoder.More() {
		fieldToken, errField := decoder.Token()
		if errField != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		if field == "models" {
			if errModels := decodeUsageModelsObject(decoder, accumulator, apiGroupKey); errModels != nil {
				return errModels
			}
		} else if errSkip := skipJSONValue(decoder); errSkip != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	return nil
}

func decodeUsageModelsObject(decoder *json.Decoder, accumulator *usageImportAccumulator, apiGroupKey string) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	for decoder.More() {
		modelToken, errModel := decoder.Token()
		if errModel != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		modelName, ok := modelToken.(string)
		if !ok {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		if errObject := decodeUsageModelObject(decoder, accumulator, apiGroupKey, modelName); errObject != nil {
			return errObject
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	return nil
}

func decodeUsageModelObject(decoder *json.Decoder, accumulator *usageImportAccumulator, apiGroupKey, modelName string) error {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	totalRequests := int64(0)
	detailCount := 0
	for decoder.More() {
		fieldToken, errField := decoder.Token()
		if errField != nil {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		switch field {
		case "total_requests":
			if errDecode := decoder.Decode(&totalRequests); errDecode != nil {
				return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
			}
		case "details":
			count, errDetails := decodeUsageDetailsArray(decoder, accumulator, apiGroupKey, modelName)
			if errDetails != nil {
				return errDetails
			}
			detailCount += count
		default:
			if errSkip := skipJSONValue(decoder); errSkip != nil {
				return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
			}
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	if totalRequests > 0 && detailCount == 0 {
		accumulator.modelsWithoutDetails[usageModelKey{
			apiGroupKey: usageSnapshotDimension(apiGroupKey),
			model:       usageSnapshotDimension(modelName),
		}] = struct{}{}
	}
	return nil
}

func decodeUsageDetailsArray(decoder *json.Decoder, accumulator *usageImportAccumulator, apiGroupKey, modelName string) (int, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return 0, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	count := 0
	for decoder.More() {
		var detail repodto.RequestDetail
		if errDecode := decoder.Decode(&detail); errDecode != nil {
			return 0, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
		}
		if errAppend := accumulator.append(apiGroupKey, modelName, detail); errAppend != nil {
			return 0, errAppend
		}
		count++
	}
	if _, err = decoder.Token(); err != nil {
		return 0, fmt.Errorf("%w: invalid json", ErrInvalidUsageImportJSON)
	}
	return count, nil
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			if _, err = decoder.Token(); err != nil {
				return err
			}
			if err = skipJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err = skipJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected json delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}

func usageEventFromImportDetail(apiGroupKey, modelName string, detail repodto.RequestDetail) (entities.UsageEvent, error) {
	if detail.Timestamp.IsZero() {
		return entities.UsageEvent{}, fmt.Errorf("%w: request detail timestamp is required", ErrInvalidUsageImportSnapshot)
	}
	tokens := normalizeTokens(detail.Tokens)
	return entities.UsageEvent{
		EventKey:        BuildEventKey(apiGroupKey, modelName, detail.Timestamp, detail.Source, detail.AuthIndex, detail.Failed, tokens),
		APIGroupKey:     strings.TrimSpace(apiGroupKey),
		Model:           strings.TrimSpace(modelName),
		Timestamp:       detail.Timestamp.UTC(),
		Source:          strings.TrimSpace(detail.Source),
		AuthIndex:       strings.TrimSpace(detail.AuthIndex),
		Failed:          detail.Failed,
		LatencyMS:       detail.LatencyMS,
		InputTokens:     tokens.InputTokens,
		OutputTokens:    tokens.OutputTokens,
		ReasoningTokens: tokens.ReasoningTokens,
		CachedTokens:    tokens.CachedTokens,
		TotalTokens:     tokens.TotalTokens,
	}, nil
}

func usageSnapshotDimension(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}
