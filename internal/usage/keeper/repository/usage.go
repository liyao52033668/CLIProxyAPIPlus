package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"gorm.io/gorm"
)

func BuildUsageSnapshot(db *gorm.DB) (*dto.StatisticsSnapshot, error) {
	return BuildUsageSnapshotWithFilter(db, dto.UsageQueryFilter{})
}

// ListUsageEventsWithFilter counts matching events and loads only the requested page.
func ListUsageEventsWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.UsageEventsPageRecord, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	baseQuery := applyUsageEventListQuery(queryUsageEvents(db), filter)

	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, fmt.Errorf("count usage events: %w", err)
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = filter.Limit
	}
	if pageSize <= 0 {
		pageSize = dto.DefaultUsageEventsLimit
	}
	offset := filter.Offset
	if offset <= 0 {
		offset = (page - 1) * pageSize
	}
	if offset < 0 {
		offset = 0
	}

	query := applyUsageEventListQuery(db.Model(&entities.UsageEvent{}), filter)
	query = query.Select(usageEventListSelectColumns).Order("timestamp DESC, id DESC").Limit(pageSize).Offset(offset)

	var events []entities.UsageEvent
	if err := query.Find(&events).Error; err != nil {
		return nil, fmt.Errorf("load usage events: %w", err)
	}

	rows := make([]dto.UsageEventRecord, 0, len(events))
	for _, event := range events {
		rows = append(rows, dto.UsageEventRecord{
			ID:              event.ID,
			Timestamp:       event.Timestamp.UTC(),
			APIGroupKey:     strings.TrimSpace(event.APIGroupKey),
			Model:           strings.TrimSpace(event.Model),
			AuthType:        strings.TrimSpace(event.AuthType),
			Provider:        strings.TrimSpace(event.Provider),
			Source:          strings.TrimSpace(event.Source),
			AuthIndex:       strings.TrimSpace(event.AuthIndex),
			Failed:          event.Failed,
			LatencyMS:       event.LatencyMS,
			InputTokens:     event.InputTokens,
			OutputTokens:    event.OutputTokens,
			ReasoningTokens: event.ReasoningTokens,
			CachedTokens:    event.CachedTokens,
			TotalTokens:     event.TotalTokens,
		})
	}
	models, err := listUsageEventModelFilterOptions(db, filter)
	if err != nil {
		return nil, err
	}
	totalPages := 0
	if totalCount > 0 {
		totalPages = int((totalCount + int64(pageSize) - 1) / int64(pageSize))
	}
	return &dto.UsageEventsPageRecord{Events: rows, Models: models, TotalCount: totalCount, Page: page, PageSize: pageSize, TotalPages: totalPages}, nil
}

// ListUsageEventFilterOptionsWithFilter collects model candidates for the time window.
func ListUsageEventFilterOptionsWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.UsageEventFilterOptionsRecord, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	models, err := listUsageEventModelFilterOptions(db, filter)
	if err != nil {
		return nil, err
	}
	return &dto.UsageEventFilterOptionsRecord{Models: models}, nil
}

func listUsageEventModelFilterOptions(db *gorm.DB, filter dto.UsageQueryFilter) ([]string, error) {
	query := applyUsageEventFilterOptionsQuery(queryUsageEvents(db), filter)

	// Exclude blank models and keep filter options stable.
	var values []string
	if err := query.Select("DISTINCT TRIM(model)").Where("TRIM(model) <> ''").Order("TRIM(model) ASC").Pluck("model", &values).Error; err != nil {
		return nil, fmt.Errorf("load usage event model filter options: %w", err)
	}
	return values, nil
}

var usageEventListSelectColumns = []string{
	"id",
	"timestamp",
	"api_group_key",
	"model",
	"auth_type",
	"provider",
	"source",
	"auth_index",
	"failed",
	"latency_ms",
	"input_tokens",
	"output_tokens",
	"reasoning_tokens",
	"cached_tokens",
	"total_tokens",
}

func queryUsageEvents(db *gorm.DB) *gorm.DB {
	return db.Model(&entities.UsageEvent{})
}

func applyUsageQueryWindow(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	if filter.StartTime != nil {
		query = query.Where("timestamp >= ?", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		query = query.Where("timestamp <= ?", filter.EndTime.UTC())
	}
	return query
}

// Overview queries apply only the selected time window.
func applyUsageOverviewQuery(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	return applyUsageQueryWindow(query, filter)
}

// Analysis queries apply only the selected time window.
func applyUsageAnalysisTabQuery(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	return applyUsageQueryWindow(query, filter)
}

// Filter-option queries ignore active event-list filters.
func applyUsageEventFilterOptionsQuery(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	return applyUsageQueryWindow(query, filter)
}

// Event-list queries combine the time window with active list filters.
func applyUsageEventListQuery(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	query = applyUsageQueryWindow(query, filter)
	if model := strings.TrimSpace(filter.Model); model != "" {
		query = query.Where("TRIM(model) = ?", model)
	}
	if source := strings.TrimSpace(filter.Source); source != "" {
		if authIndex := strings.TrimSpace(filter.AuthIndex); authIndex != "" {
			// Keep direct repository callers compatible with source-based filtering.
			query = query.Where("(TRIM(auth_index) = ? OR TRIM(source) = ?)", authIndex, source)
		} else {
			query = query.Where("TRIM(source) = ?", source)
		}
	} else if authIndex := strings.TrimSpace(filter.AuthIndex); authIndex != "" {
		query = query.Where("TRIM(auth_index) = ?", authIndex)
	}
	switch strings.TrimSpace(filter.Result) {
	case "success":
		query = query.Where("failed = ?", false)
	case "failed":
		query = query.Where("failed = ?", true)
	}
	return query
}

type usageAnalysisAggregateRow struct {
	APIGroupKey        string
	Model              string
	TotalRequests      int64
	SuccessCount       int64
	FailureCount       int64
	InputTokens        int64
	OutputTokens       int64
	ReasoningTokens    int64
	CachedTokens       int64
	TotalTokens        int64
	TotalLatencyMS     int64
	LatencySampleCount int64
}

// ListUsageAnalysisWithFilter scans API-model aggregates once and folds API and model totals in memory.
func ListUsageAnalysisWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) ([]dto.UsageAnalysisAPIStatRecord, []dto.UsageAnalysisModelStatRecord, error) {
	if db == nil {
		return nil, nil, fmt.Errorf("database is nil")
	}

	query := applyUsageAnalysisTabQuery(db.Model(&entities.UsageEvent{}), filter)
	query = query.Select(strings.Join([]string{
		"TRIM(api_group_key) AS api_group_key",
		"TRIM(model) AS model",
		"COUNT(*) AS total_requests",
		"SUM(CASE WHEN failed THEN 0 ELSE 1 END) AS success_count",
		"SUM(CASE WHEN failed THEN 1 ELSE 0 END) AS failure_count",
		"SUM(input_tokens) AS input_tokens",
		"SUM(output_tokens) AS output_tokens",
		"SUM(reasoning_tokens) AS reasoning_tokens",
		"SUM(cached_tokens) AS cached_tokens",
		"SUM(total_tokens) AS total_tokens",
		"SUM(latency_ms) AS total_latency_ms",
		"SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_sample_count",
	}, ", "))
	query = query.Group("TRIM(api_group_key), TRIM(model)")

	var aggregateRows []usageAnalysisAggregateRow
	if err := query.Scan(&aggregateRows).Error; err != nil {
		return nil, nil, fmt.Errorf("load usage analysis api model stats: %w", err)
	}

	archiveQuery := applyUsageAggregateQueryWindow(db.Model(&entities.UsageHourlyAggregate{}), filter)
	archiveQuery = archiveQuery.Select(strings.Join([]string{
		"TRIM(api_group_key) AS api_group_key",
		"TRIM(model) AS model",
		"SUM(request_count) AS total_requests",
		"SUM(success_count) AS success_count",
		"SUM(failure_count) AS failure_count",
		"SUM(input_tokens) AS input_tokens",
		"SUM(output_tokens) AS output_tokens",
		"SUM(reasoning_tokens) AS reasoning_tokens",
		"SUM(cached_tokens) AS cached_tokens",
		"SUM(total_tokens) AS total_tokens",
		"SUM(total_latency_ms) AS total_latency_ms",
		"SUM(latency_sample_count) AS latency_sample_count",
	}, ", ")).Group("TRIM(api_group_key), TRIM(model)")
	var archivedRows []usageAnalysisAggregateRow
	if err := archiveQuery.Scan(&archivedRows).Error; err != nil {
		return nil, nil, fmt.Errorf("load archived usage analysis api model stats: %w", err)
	}
	aggregateRows = append(aggregateRows, archivedRows...)

	normalize := func(value string) string {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return "unknown"
		}
		return trimmed
	}

	apisByKey := make(map[string]dto.UsageAnalysisAPIStatRecord)
	modelsByAPI := make(map[string]map[string]dto.UsageAnalysisModelStatRecord)
	modelsByKey := make(map[string]dto.UsageAnalysisModelStatRecord)
	for _, row := range aggregateRows {
		apiKey := strings.TrimSpace(row.APIGroupKey)
		modelKey := strings.TrimSpace(row.Model)
		apiModels := modelsByAPI[apiKey]
		if apiModels == nil {
			apiModels = make(map[string]dto.UsageAnalysisModelStatRecord)
		}
		modelStat := apiModels[modelKey]
		modelStat.Model = normalize(modelKey)
		modelStat.TotalRequests += row.TotalRequests
		modelStat.SuccessCount += row.SuccessCount
		modelStat.FailureCount += row.FailureCount
		modelStat.InputTokens += row.InputTokens
		modelStat.OutputTokens += row.OutputTokens
		modelStat.ReasoningTokens += row.ReasoningTokens
		modelStat.CachedTokens += row.CachedTokens
		modelStat.TotalTokens += row.TotalTokens
		modelStat.TotalLatencyMS += row.TotalLatencyMS
		modelStat.LatencySampleCount += row.LatencySampleCount
		apiModels[modelKey] = modelStat
		modelsByAPI[apiKey] = apiModels

		apiStat := apisByKey[apiKey]
		apiStat.APIGroupKey = normalize(apiKey)
		apiStat.DisplayName = apiStat.APIGroupKey
		apiStat.TotalRequests += row.TotalRequests
		apiStat.SuccessCount += row.SuccessCount
		apiStat.FailureCount += row.FailureCount
		apiStat.InputTokens += row.InputTokens
		apiStat.OutputTokens += row.OutputTokens
		apiStat.ReasoningTokens += row.ReasoningTokens
		apiStat.CachedTokens += row.CachedTokens
		apiStat.TotalTokens += row.TotalTokens
		apisByKey[apiKey] = apiStat

		globalModel := modelsByKey[modelKey]
		globalModel.Model = normalize(modelKey)
		globalModel.TotalRequests += row.TotalRequests
		globalModel.SuccessCount += row.SuccessCount
		globalModel.FailureCount += row.FailureCount
		globalModel.InputTokens += row.InputTokens
		globalModel.OutputTokens += row.OutputTokens
		globalModel.ReasoningTokens += row.ReasoningTokens
		globalModel.CachedTokens += row.CachedTokens
		globalModel.TotalTokens += row.TotalTokens
		globalModel.TotalLatencyMS += row.TotalLatencyMS
		globalModel.LatencySampleCount += row.LatencySampleCount
		modelsByKey[modelKey] = globalModel
	}

	resultAPIs := make([]dto.UsageAnalysisAPIStatRecord, 0, len(apisByKey))
	for apiKey, apiStat := range apisByKey {
		apiModels := modelsByAPI[apiKey]
		apiStat.Models = make([]dto.UsageAnalysisModelStatRecord, 0, len(apiModels))
		for _, modelStat := range apiModels {
			apiStat.Models = append(apiStat.Models, modelStat)
		}
		sort.Slice(apiStat.Models, func(i, j int) bool {
			if apiStat.Models[i].TotalRequests != apiStat.Models[j].TotalRequests {
				return apiStat.Models[i].TotalRequests > apiStat.Models[j].TotalRequests
			}
			return apiStat.Models[i].Model < apiStat.Models[j].Model
		})
		resultAPIs = append(resultAPIs, apiStat)
	}
	sort.Slice(resultAPIs, func(i, j int) bool {
		if resultAPIs[i].TotalRequests != resultAPIs[j].TotalRequests {
			return resultAPIs[i].TotalRequests > resultAPIs[j].TotalRequests
		}
		return resultAPIs[i].APIGroupKey < resultAPIs[j].APIGroupKey
	})

	modelRows := make([]dto.UsageAnalysisModelStatRecord, 0, len(modelsByKey))
	for _, modelStat := range modelsByKey {
		modelRows = append(modelRows, modelStat)
	}
	sort.Slice(modelRows, func(i, j int) bool {
		if modelRows[i].TotalRequests != modelRows[j].TotalRequests {
			return modelRows[i].TotalRequests > modelRows[j].TotalRequests
		}
		return modelRows[i].Model < modelRows[j].Model
	})

	return resultAPIs, modelRows, nil
}

// usageEventStreamBatchSize bounds each page when streaming usage_events so
// overview/snapshot never materialize the full table with SELECT *.
const usageEventStreamBatchSize = 1000

// Snapshot streams matching events in pages and aggregates them in memory.
// Export still needs request details, so details are included.
func BuildUsageSnapshotWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.StatisticsSnapshot, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	snapshot := newEmptyUsageSnapshot()
	if err := streamUsageHourlyAggregatesWithFilter(db, filter, func(aggregate entities.UsageHourlyAggregate) error {
		applyUsageAggregateToSnapshot(snapshot, aggregate)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := streamUsageEventsWithFilter(db, filter, usageSnapshotSelectColumns, func(event entities.UsageEvent) error {
		applyUsageEventToSnapshot(snapshot, event, true)
		return nil
	}); err != nil {
		return nil, err
	}
	finalizeUsageSnapshot(snapshot, true)
	return snapshot, nil
}

// BuildUsageAggregateSnapshotWithFilter streams matching events without request details.
func BuildUsageAggregateSnapshotWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.StatisticsSnapshot, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	snapshot := newEmptyUsageSnapshot()
	if err := streamUsageHourlyAggregatesWithFilter(db, filter, func(aggregate entities.UsageHourlyAggregate) error {
		applyUsageAggregateToSnapshot(snapshot, aggregate)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := streamUsageEventsWithFilter(db, filter, usageAggregateSelectColumns, func(event entities.UsageEvent) error {
		applyUsageEventToSnapshot(snapshot, event, false)
		return nil
	}); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Overview streams matching events in pages and builds summary/series/health
// without loading the full result set or per-request details.
func BuildUsageOverviewWithFilter(db *gorm.DB, filter dto.UsageQueryFilter) (*dto.UsageOverviewRecord, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	pricingByModel, err := loadPriceSettingsByModel(db)
	if err != nil {
		return nil, err
	}

	overview := newUsageOverviewRecord(filter)
	bucketByDay := shouldBucketUsageOverviewByDay(filter, overview.Summary.WindowMinutes)
	latestHourlyStart := latestHourlySeriesStart(filter)
	if err := streamUsageHourlyAggregatesWithFilter(db, filter, func(aggregate entities.UsageHourlyAggregate) error {
		applyUsageAggregateToSnapshot(overview.Usage, aggregate)
		applyUsageAggregateToOverview(overview, aggregate, bucketByDay, latestHourlyStart, pricingByModel)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := streamUsageEventsWithFilter(db, filter, usageOverviewSelectColumns, func(event entities.UsageEvent) error {
		applyUsageEventToSnapshot(overview.Usage, event, false)
		applyUsageEventToOverview(overview, event, bucketByDay, latestHourlyStart, pricingByModel)
		return nil
	}); err != nil {
		return nil, err
	}
	finalizeUsageOverview(overview, false, filter.StartTime, filter.EndTime, bucketByDay)
	return overview, nil
}

func buildUsageOverviewFromEvents(events []entities.UsageEvent, filter dto.UsageQueryFilter, pricingByModel map[string]entities.ModelPriceSetting) *dto.UsageOverviewRecord {
	windowMinutes := computeWindowMinutes(filter)
	bucketByDay := shouldBucketUsageOverviewByDay(filter, windowMinutes)
	latestHourlyStart := latestHourlySeriesStart(filter)
	overview := newUsageOverviewRecord(filter)
	if len(events) == 0 {
		return overview
	}

	// In-memory helper used by unit tests; keep details off the overview path.
	for _, event := range events {
		applyUsageEventToSnapshot(overview.Usage, event, false)
		applyUsageEventToOverview(overview, event, bucketByDay, latestHourlyStart, pricingByModel)
	}
	finalizeUsageOverview(overview, false, filter.StartTime, filter.EndTime, bucketByDay)
	return overview
}

func newEmptyUsageSnapshot() *dto.StatisticsSnapshot {
	return &dto.StatisticsSnapshot{
		APIs:           map[string]dto.APISnapshot{},
		RequestsByDay:  map[string]int64{},
		RequestsByHour: map[string]int64{},
		TokensByDay:    map[string]int64{},
		TokensByHour:   map[string]int64{},
	}
}

func newUsageOverviewRecord(filter dto.UsageQueryFilter) *dto.UsageOverviewRecord {
	windowMinutes := computeWindowMinutes(filter)
	return &dto.UsageOverviewRecord{
		Usage: newEmptyUsageSnapshot(),
		Summary: dto.UsageOverviewSummaryRecord{
			WindowMinutes: windowMinutes,
			CostAvailable: true,
		},
		Series:       newUsageOverviewSeriesRecord(),
		HourlySeries: newUsageOverviewSeriesRecord(),
		DailySeries:  newUsageOverviewSeriesRecord(),
		Health:       buildUsageOverviewHealth(filter),
		KeyStats:     newUsageKeyStatsRecord(),
	}
}

func newUsageKeyStatsRecord() dto.UsageKeyStatsRecord {
	return dto.UsageKeyStatsRecord{
		BySource:    map[string]dto.UsageKeyCountRecord{},
		ByAuthIndex: map[string]dto.UsageKeyCountRecord{},
		Credentials: map[dto.UsageCredentialKey]dto.UsageKeyCountRecord{},
	}
}

var usageAggregateSelectColumns = []string{
	"id",
	"api_group_key",
	"model",
	"timestamp",
	"failed",
	"total_tokens",
}

var usageOverviewSelectColumns = []string{
	"id",
	"api_group_key",
	"model",
	"timestamp",
	"source",
	"auth_index",
	"failed",
	"input_tokens",
	"output_tokens",
	"reasoning_tokens",
	"cached_tokens",
	"total_tokens",
}

var usageSnapshotSelectColumns = []string{
	"id",
	"api_group_key",
	"model",
	"timestamp",
	"source",
	"auth_index",
	"failed",
	"latency_ms",
	"input_tokens",
	"output_tokens",
	"reasoning_tokens",
	"cached_tokens",
	"total_tokens",
}

// StreamUsageEventsForExport reads export fields in API, model, and timestamp order.
func StreamUsageEventsForExport(ctx context.Context, db *gorm.DB, filter dto.UsageQueryFilter, handle func(entities.UsageEvent) error) (err error) {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if handle == nil {
		return fmt.Errorf("usage event handler is nil")
	}

	query := applyUsageQueryWindow(db.WithContext(ctx).Model(&entities.UsageEvent{}), filter)
	rows, errRows := query.
		Select(usageSnapshotSelectColumns).
		Order("CASE WHEN TRIM(api_group_key) = '' THEN 'unknown' ELSE TRIM(api_group_key) END ASC, CASE WHEN TRIM(model) = '' THEN 'unknown' ELSE TRIM(model) END ASC, timestamp ASC, id ASC").
		Rows()
	if errRows != nil {
		return fmt.Errorf("open usage event export stream: %w", errRows)
	}
	defer func() {
		if errClose := rows.Close(); err == nil && errClose != nil {
			err = fmt.Errorf("close usage event export stream: %w", errClose)
		}
	}()

	for rows.Next() {
		var event entities.UsageEvent
		if errScan := db.ScanRows(rows, &event); errScan != nil {
			return fmt.Errorf("scan usage event export row: %w", errScan)
		}
		if errHandle := handle(event); errHandle != nil {
			return errHandle
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("read usage event export stream: %w", errRows)
	}
	return nil
}

// streamUsageEventsWithFilter pages through usage_events with keyset pagination
// so callers never issue an unbounded SELECT * ... ORDER BY timestamp.
func streamUsageEventsWithFilter(db *gorm.DB, filter dto.UsageQueryFilter, columns []string, handle func(entities.UsageEvent) error) error {
	if handle == nil {
		return fmt.Errorf("usage event handler is nil")
	}

	var (
		lastTimestamp time.Time
		lastID        uint
		hasCursor     bool
	)

	for {
		query := applyUsageOverviewQuery(db.Model(&entities.UsageEvent{}), filter)
		if len(columns) > 0 {
			query = query.Select(columns)
		}
		if hasCursor {
			query = query.Where("(timestamp > ?) OR (timestamp = ? AND id > ?)", lastTimestamp, lastTimestamp, lastID)
		}
		query = query.Order("timestamp asc, id asc").Limit(usageEventStreamBatchSize)

		var events []entities.UsageEvent
		if err := query.Find(&events).Error; err != nil {
			return fmt.Errorf("load usage events: %w", err)
		}
		if len(events) == 0 {
			return nil
		}

		for _, event := range events {
			if err := handle(event); err != nil {
				return err
			}
		}

		last := events[len(events)-1]
		lastTimestamp = last.Timestamp
		lastID = last.ID
		hasCursor = true
		if len(events) < usageEventStreamBatchSize {
			return nil
		}
	}
}

func buildUsageSnapshotFromEvents(events []entities.UsageEvent) *dto.StatisticsSnapshot {
	snapshot := newEmptyUsageSnapshot()
	if len(events) == 0 {
		return snapshot
	}

	for _, event := range events {
		applyUsageEventToSnapshot(snapshot, event, true)
	}
	finalizeUsageSnapshot(snapshot, true)
	return snapshot
}

func applyUsageEventToSnapshot(snapshot *dto.StatisticsSnapshot, event entities.UsageEvent, includeDetails bool) {
	apiKey := normalizeUsageOverviewDimension(event.APIGroupKey)
	modelName := normalizeUsageOverviewDimension(event.Model)

	apiSnapshot := snapshot.APIs[apiKey]
	if apiSnapshot.Models == nil {
		apiSnapshot.Models = map[string]dto.ModelSnapshot{}
	}

	modelSnapshot := apiSnapshot.Models[modelName]
	if includeDetails {
		detail := dto.RequestDetail{
			Timestamp: event.Timestamp.UTC(),
			LatencyMS: event.LatencyMS,
			Source:    strings.TrimSpace(event.Source),
			AuthIndex: strings.TrimSpace(event.AuthIndex),
			Failed:    event.Failed,
			Tokens: dto.TokenStats{
				InputTokens:     event.InputTokens,
				OutputTokens:    event.OutputTokens,
				ReasoningTokens: event.ReasoningTokens,
				CachedTokens:    event.CachedTokens,
				TotalTokens:     event.TotalTokens,
			},
		}
		modelSnapshot.Details = append(modelSnapshot.Details, detail)
	}
	modelSnapshot.TotalRequests++
	modelSnapshot.TotalTokens += event.TotalTokens
	apiSnapshot.TotalRequests++
	apiSnapshot.TotalTokens += event.TotalTokens
	snapshot.TotalRequests++
	snapshot.TotalTokens += event.TotalTokens
	if event.Failed {
		modelSnapshot.FailureCount++
		apiSnapshot.FailureCount++
		snapshot.FailureCount++
	} else {
		modelSnapshot.SuccessCount++
		apiSnapshot.SuccessCount++
		snapshot.SuccessCount++
	}

	dayKey := event.Timestamp.In(time.Local).Format("2006-01-02")
	hourKey := event.Timestamp.UTC().Format("2006-01-02T15:00:00Z")
	snapshot.RequestsByDay[dayKey]++
	snapshot.RequestsByHour[hourKey]++
	snapshot.TokensByDay[dayKey] += event.TotalTokens
	snapshot.TokensByHour[hourKey] += event.TotalTokens

	apiSnapshot.Models[modelName] = modelSnapshot
	snapshot.APIs[apiKey] = apiSnapshot
}

func applyUsageAggregateToSnapshot(snapshot *dto.StatisticsSnapshot, aggregate entities.UsageHourlyAggregate) {
	apiKey := normalizeUsageOverviewDimension(aggregate.APIGroupKey)
	modelName := normalizeUsageOverviewDimension(aggregate.Model)
	apiSnapshot := snapshot.APIs[apiKey]
	if apiSnapshot.Models == nil {
		apiSnapshot.Models = map[string]dto.ModelSnapshot{}
	}
	modelSnapshot := apiSnapshot.Models[modelName]
	modelSnapshot.TotalRequests += aggregate.RequestCount
	modelSnapshot.SuccessCount += aggregate.SuccessCount
	modelSnapshot.FailureCount += aggregate.FailureCount
	modelSnapshot.TotalTokens += aggregate.TotalTokens
	apiSnapshot.TotalRequests += aggregate.RequestCount
	apiSnapshot.SuccessCount += aggregate.SuccessCount
	apiSnapshot.FailureCount += aggregate.FailureCount
	apiSnapshot.TotalTokens += aggregate.TotalTokens
	snapshot.TotalRequests += aggregate.RequestCount
	snapshot.SuccessCount += aggregate.SuccessCount
	snapshot.FailureCount += aggregate.FailureCount
	snapshot.TotalTokens += aggregate.TotalTokens

	dayKey := aggregate.BucketStart.In(time.Local).Format("2006-01-02")
	hourKey := aggregate.BucketStart.UTC().Format("2006-01-02T15:00:00Z")
	snapshot.RequestsByDay[dayKey] += aggregate.RequestCount
	snapshot.RequestsByHour[hourKey] += aggregate.RequestCount
	snapshot.TokensByDay[dayKey] += aggregate.TotalTokens
	snapshot.TokensByHour[hourKey] += aggregate.TotalTokens
	apiSnapshot.Models[modelName] = modelSnapshot
	snapshot.APIs[apiKey] = apiSnapshot
}

func finalizeUsageSnapshot(snapshot *dto.StatisticsSnapshot, includeDetails bool) {
	if !includeDetails {
		return
	}
	for apiKey, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			sort.Slice(modelSnapshot.Details, func(i, j int) bool {
				return modelSnapshot.Details[i].Timestamp.Before(modelSnapshot.Details[j].Timestamp)
			})
			apiSnapshot.Models[modelName] = modelSnapshot
		}
		snapshot.APIs[apiKey] = apiSnapshot
	}
}

func newUsageOverviewSeriesRecord() dto.UsageOverviewSeriesRecord {
	return dto.UsageOverviewSeriesRecord{
		Requests:        map[string]int64{},
		Tokens:          map[string]int64{},
		RPM:             map[string]float64{},
		TPM:             map[string]float64{},
		Cost:            map[string]float64{},
		InputTokens:     map[string]int64{},
		OutputTokens:    map[string]int64{},
		CachedTokens:    map[string]int64{},
		ReasoningTokens: map[string]int64{},
		Models:          map[string]dto.UsageOverviewSeriesRecord{},
	}
}

func applyUsageEventToOverviewSeries(series *dto.UsageOverviewSeriesRecord, event entities.UsageEvent, cost float64, bucketKey string, bucketMinutes int64) {
	series.Requests[bucketKey]++
	series.Tokens[bucketKey] += event.TotalTokens
	series.Cost[bucketKey] += cost
	series.InputTokens[bucketKey] += event.InputTokens
	series.OutputTokens[bucketKey] += event.OutputTokens
	series.CachedTokens[bucketKey] += event.CachedTokens
	series.ReasoningTokens[bucketKey] += event.ReasoningTokens
	series.RPM[bucketKey] = float64(series.Requests[bucketKey]) / float64(bucketMinutes)
	series.TPM[bucketKey] = float64(series.Tokens[bucketKey]) / float64(bucketMinutes)

	modelName := normalizeUsageOverviewDimension(event.Model)
	modelSeries := series.Models[modelName]
	if modelSeries.Requests == nil {
		modelSeries = newUsageOverviewSeriesRecord()
	}
	modelSeries.Requests[bucketKey]++
	modelSeries.Tokens[bucketKey] += event.TotalTokens
	modelSeries.Cost[bucketKey] += cost
	modelSeries.InputTokens[bucketKey] += event.InputTokens
	modelSeries.OutputTokens[bucketKey] += event.OutputTokens
	modelSeries.CachedTokens[bucketKey] += event.CachedTokens
	modelSeries.ReasoningTokens[bucketKey] += event.ReasoningTokens
	modelSeries.RPM[bucketKey] = float64(modelSeries.Requests[bucketKey]) / float64(bucketMinutes)
	modelSeries.TPM[bucketKey] = float64(modelSeries.Tokens[bucketKey]) / float64(bucketMinutes)
	series.Models[modelName] = modelSeries
}

func applyUsageAggregateToOverviewSeries(series *dto.UsageOverviewSeriesRecord, aggregate entities.UsageHourlyAggregate, cost float64, bucketKey string, bucketMinutes int64) {
	series.Requests[bucketKey] += aggregate.RequestCount
	series.Tokens[bucketKey] += aggregate.TotalTokens
	series.Cost[bucketKey] += cost
	series.InputTokens[bucketKey] += aggregate.InputTokens
	series.OutputTokens[bucketKey] += aggregate.OutputTokens
	series.CachedTokens[bucketKey] += aggregate.CachedTokens
	series.ReasoningTokens[bucketKey] += aggregate.ReasoningTokens
	series.RPM[bucketKey] = float64(series.Requests[bucketKey]) / float64(bucketMinutes)
	series.TPM[bucketKey] = float64(series.Tokens[bucketKey]) / float64(bucketMinutes)

	modelName := normalizeUsageOverviewDimension(aggregate.Model)
	modelSeries := series.Models[modelName]
	if modelSeries.Requests == nil {
		modelSeries = newUsageOverviewSeriesRecord()
	}
	modelSeries.Requests[bucketKey] += aggregate.RequestCount
	modelSeries.Tokens[bucketKey] += aggregate.TotalTokens
	modelSeries.Cost[bucketKey] += cost
	modelSeries.InputTokens[bucketKey] += aggregate.InputTokens
	modelSeries.OutputTokens[bucketKey] += aggregate.OutputTokens
	modelSeries.CachedTokens[bucketKey] += aggregate.CachedTokens
	modelSeries.ReasoningTokens[bucketKey] += aggregate.ReasoningTokens
	modelSeries.RPM[bucketKey] = float64(modelSeries.Requests[bucketKey]) / float64(bucketMinutes)
	modelSeries.TPM[bucketKey] = float64(modelSeries.Tokens[bucketKey]) / float64(bucketMinutes)
	series.Models[modelName] = modelSeries
}

func usageEventRequiresPricing(event entities.UsageEvent) bool {
	return event.InputTokens > 0 || event.OutputTokens > 0 || event.CachedTokens > 0
}

func usageAggregateRequiresPricing(aggregate entities.UsageHourlyAggregate) bool {
	return aggregate.InputTokens > 0 || aggregate.OutputTokens > 0 || aggregate.CachedTokens > 0
}

func applyUsageEventToOverview(overview *dto.UsageOverviewRecord, event entities.UsageEvent, bucketByDay bool, latestHourlyStart *time.Time, pricingByModel map[string]entities.ModelPriceSetting) {
	overview.Summary.CachedTokens += event.CachedTokens
	overview.Summary.ReasoningTokens += event.ReasoningTokens
	if event.Failed {
		overview.Health.TotalFailure++
	} else {
		overview.Health.TotalSuccess++
	}
	pricing, ok := pricingByModel[strings.TrimSpace(event.Model)]
	if !ok && usageEventRequiresPricing(event) {
		overview.Summary.CostAvailable = false
	}
	cost := calculateUsageEventCost(event, pricing)
	overview.Summary.TotalCost += cost
	applyUsageEventToKeyStats(&overview.KeyStats, event, cost)

	bucketKey, bucketMinutes := usageOverviewBucket(event.Timestamp.UTC(), bucketByDay)
	applyUsageEventToOverviewSeries(&overview.Series, event, cost, bucketKey, bucketMinutes)

	hourKey, hourMinutes := usageOverviewBucket(event.Timestamp.UTC(), false)
	if latestHourlyStart == nil || !event.Timestamp.UTC().Before(*latestHourlyStart) {
		applyUsageEventToOverviewSeries(&overview.HourlySeries, event, cost, hourKey, hourMinutes)
	}

	dayKey, dayMinutes := usageOverviewBucket(event.Timestamp.UTC(), true)
	applyUsageEventToOverviewSeries(&overview.DailySeries, event, cost, dayKey, dayMinutes)
	updateUsageOverviewHealthBlock(overview.Health.BlockDetails, event)
}

func applyUsageAggregateToOverview(overview *dto.UsageOverviewRecord, aggregate entities.UsageHourlyAggregate, bucketByDay bool, latestHourlyStart *time.Time, pricingByModel map[string]entities.ModelPriceSetting) {
	overview.Summary.CachedTokens += aggregate.CachedTokens
	overview.Summary.ReasoningTokens += aggregate.ReasoningTokens
	overview.Health.TotalSuccess += aggregate.SuccessCount
	overview.Health.TotalFailure += aggregate.FailureCount
	pricing, ok := pricingByModel[strings.TrimSpace(aggregate.Model)]
	if !ok && usageAggregateRequiresPricing(aggregate) {
		overview.Summary.CostAvailable = false
	}
	cost := calculateUsageAggregateCost(aggregate, pricing)
	overview.Summary.TotalCost += cost
	applyUsageAggregateToKeyStats(&overview.KeyStats, aggregate, cost)

	bucketKey, bucketMinutes := usageOverviewBucket(aggregate.BucketStart.UTC(), bucketByDay)
	applyUsageAggregateToOverviewSeries(&overview.Series, aggregate, cost, bucketKey, bucketMinutes)
	hourKey, hourMinutes := usageOverviewBucket(aggregate.BucketStart.UTC(), false)
	if latestHourlyStart == nil || !aggregate.BucketStart.UTC().Before(*latestHourlyStart) {
		applyUsageAggregateToOverviewSeries(&overview.HourlySeries, aggregate, cost, hourKey, hourMinutes)
	}
	dayKey, dayMinutes := usageOverviewBucket(aggregate.BucketStart.UTC(), true)
	applyUsageAggregateToOverviewSeries(&overview.DailySeries, aggregate, cost, dayKey, dayMinutes)
	updateUsageOverviewHealthBlockWithAggregate(overview.Health.BlockDetails, aggregate)
}

func applyUsageEventToKeyStats(stats *dto.UsageKeyStatsRecord, event entities.UsageEvent, cost float64) {
	if stats == nil {
		return
	}
	if stats.BySource == nil {
		stats.BySource = map[string]dto.UsageKeyCountRecord{}
	}
	if stats.ByAuthIndex == nil {
		stats.ByAuthIndex = map[string]dto.UsageKeyCountRecord{}
	}
	if stats.Credentials == nil {
		stats.Credentials = map[dto.UsageCredentialKey]dto.UsageKeyCountRecord{}
	}

	source := strings.TrimSpace(event.Source)
	authIndex := strings.TrimSpace(event.AuthIndex)
	if source != "" {
		count := stats.BySource[source]
		if event.Failed {
			count.Failure++
		} else {
			count.Success++
		}
		count.Tokens += event.TotalTokens
		count.Cost += cost
		stats.BySource[source] = count
	}
	if authIndex != "" {
		count := stats.ByAuthIndex[authIndex]
		if event.Failed {
			count.Failure++
		} else {
			count.Success++
		}
		count.Tokens += event.TotalTokens
		count.Cost += cost
		stats.ByAuthIndex[authIndex] = count
	}
	if source != "" || authIndex != "" {
		key := dto.UsageCredentialKey{Source: source, AuthIndex: authIndex}
		count := stats.Credentials[key]
		if event.Failed {
			count.Failure++
		} else {
			count.Success++
		}
		count.Tokens += event.TotalTokens
		count.Cost += cost
		stats.Credentials[key] = count
	}
}

func applyUsageAggregateToKeyStats(stats *dto.UsageKeyStatsRecord, aggregate entities.UsageHourlyAggregate, cost float64) {
	if stats == nil {
		return
	}
	source := strings.TrimSpace(aggregate.Source)
	authIndex := strings.TrimSpace(aggregate.AuthIndex)
	apply := func(count dto.UsageKeyCountRecord) dto.UsageKeyCountRecord {
		count.Success += aggregate.SuccessCount
		count.Failure += aggregate.FailureCount
		count.Tokens += aggregate.TotalTokens
		count.Cost += cost
		return count
	}
	if source != "" {
		stats.BySource[source] = apply(stats.BySource[source])
	}
	if authIndex != "" {
		stats.ByAuthIndex[authIndex] = apply(stats.ByAuthIndex[authIndex])
	}
	if source != "" || authIndex != "" {
		key := dto.UsageCredentialKey{Source: source, AuthIndex: authIndex}
		stats.Credentials[key] = apply(stats.Credentials[key])
	}
}

func finalizeUsageOverview(overview *dto.UsageOverviewRecord, includeDetails bool, startTime, endTime *time.Time, bucketByDay bool) {
	finalizeUsageSnapshot(overview.Usage, includeDetails)
	overview.Summary.RequestCount = overview.Usage.TotalRequests
	overview.Summary.TokenCount = overview.Usage.TotalTokens
	if overview.Summary.WindowMinutes > 0 {
		overview.Summary.RPM = float64(overview.Summary.RequestCount) / float64(overview.Summary.WindowMinutes)
		overview.Summary.TPM = float64(overview.Summary.TokenCount) / float64(overview.Summary.WindowMinutes)
	}
	if total := overview.Health.TotalSuccess + overview.Health.TotalFailure; total > 0 {
		overview.Health.SuccessRate = (float64(overview.Health.TotalSuccess) / float64(total)) * 100
	}

	if startTime != nil && endTime != nil {
		fillEmptyTimePointsForAllSeries(overview, *startTime, *endTime, bucketByDay)
	}
}

func fillEmptyTimePointsForAllSeries(overview *dto.UsageOverviewRecord, startTime, endTime time.Time, bucketByDay bool) {
	fillEmptyTimePointsForSeries(&overview.Series, startTime, endTime, bucketByDay)
	fillEmptyTimePointsForSeries(&overview.HourlySeries, startTime, endTime, false)
	fillEmptyTimePointsForSeries(&overview.DailySeries, startTime, endTime, true)
}

func fillEmptyTimePointsForSeries(series *dto.UsageOverviewSeriesRecord, startTime, endTime time.Time, byDay bool) {
	if len(series.Models) == 0 {
		return
	}

	var currentTime time.Time
	var step time.Duration
	var format string

	if byDay {
		currentTime = startTime.In(time.Local).Truncate(24 * time.Hour)
		endTime = endTime.In(time.Local).Truncate(24 * time.Hour)
		step = 24 * time.Hour
		format = "2006-01-02"
	} else {
		currentTime = startTime.UTC().Truncate(time.Hour)
		endTime = endTime.UTC().Truncate(time.Hour)
		step = time.Hour
		format = "2006-01-02T15:00:00Z"
	}

	existingKeys := make(map[string]bool)
	for key := range series.Requests {
		existingKeys[key] = true
	}

	for !currentTime.After(endTime) {
		key := currentTime.Format(format)

		if existingKeys[key] {
			for modelName, modelSeries := range series.Models {
				if modelSeries.Requests == nil {
					modelSeries.Requests = map[string]int64{}
				}
				if modelSeries.Tokens == nil {
					modelSeries.Tokens = map[string]int64{}
				}
				if modelSeries.RPM == nil {
					modelSeries.RPM = map[string]float64{}
				}
				if modelSeries.TPM == nil {
					modelSeries.TPM = map[string]float64{}
				}
				if modelSeries.Cost == nil {
					modelSeries.Cost = map[string]float64{}
				}
				if modelSeries.InputTokens == nil {
					modelSeries.InputTokens = map[string]int64{}
				}
				if modelSeries.OutputTokens == nil {
					modelSeries.OutputTokens = map[string]int64{}
				}
				if modelSeries.CachedTokens == nil {
					modelSeries.CachedTokens = map[string]int64{}
				}
				if modelSeries.ReasoningTokens == nil {
					modelSeries.ReasoningTokens = map[string]int64{}
				}

				if _, exists := modelSeries.Requests[key]; !exists {
					modelSeries.Requests[key] = 0
					modelSeries.Tokens[key] = 0
					modelSeries.RPM[key] = 0
					modelSeries.TPM[key] = 0
					modelSeries.Cost[key] = 0
					modelSeries.InputTokens[key] = 0
					modelSeries.OutputTokens[key] = 0
					modelSeries.CachedTokens[key] = 0
					modelSeries.ReasoningTokens[key] = 0
					series.Models[modelName] = modelSeries
				}
			}
		}

		currentTime = currentTime.Add(step)
	}
}

func normalizeUsageOverviewDimension(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func loadPriceSettingsByModel(db *gorm.DB) (map[string]entities.ModelPriceSetting, error) {
	settings, err := ListModelPriceSettings(db)
	if err != nil {
		return nil, err
	}
	result := make(map[string]entities.ModelPriceSetting, len(settings))
	for _, setting := range settings {
		result[strings.TrimSpace(setting.Model)] = setting
	}
	return result, nil
}

func calculateUsageEventCost(event entities.UsageEvent, pricing entities.ModelPriceSetting) float64 {
	return calculateUsageCost(event.InputTokens, event.OutputTokens, event.CachedTokens, pricing)
}

func calculateUsageAggregateCost(aggregate entities.UsageHourlyAggregate, pricing entities.ModelPriceSetting) float64 {
	return calculateUsageCost(aggregate.InputTokens, aggregate.OutputTokens, aggregate.CachedTokens, pricing)
}

func calculateUsageCost(inputTokens, outputTokens, cachedTokens int64, pricing entities.ModelPriceSetting) float64 {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	promptTokens := inputTokens - cachedTokens
	if promptTokens < 0 {
		promptTokens = 0
	}
	return (float64(promptTokens)/1_000_000.0)*pricing.PromptPricePer1M +
		(float64(outputTokens)/1_000_000.0)*pricing.CompletionPricePer1M +
		(float64(cachedTokens)/1_000_000.0)*pricing.CachePricePer1M
}

const usageOverviewDailyBucketThresholdMinutes int64 = 7 * 24 * 60

func computeWindowMinutes(filter dto.UsageQueryFilter) int64 {
	if filter.StartTime == nil || filter.EndTime == nil {
		return 0
	}
	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	if end.Before(start) {
		return 0
	}
	minutes := int64(end.Sub(start) / time.Minute)
	if end.Sub(start)%time.Minute != 0 {
		minutes++
	}
	if minutes < 1 {
		return 1
	}
	return minutes
}

func shouldBucketUsageOverviewByDay(filter dto.UsageQueryFilter, windowMinutes int64) bool {
	if filter.Range == "all" || filter.Range == "7d" {
		return true
	}
	return windowMinutes >= usageOverviewDailyBucketThresholdMinutes
}

func usageOverviewBucket(timestamp time.Time, byDay bool) (string, int64) {
	if byDay {
		return timestamp.In(time.Local).Format("2006-01-02"), 24 * 60
	}
	return timestamp.UTC().Format("2006-01-02T15:00:00Z"), 60
}

func latestHourlySeriesStart(filter dto.UsageQueryFilter) *time.Time {
	if filter.EndTime == nil {
		return nil
	}
	currentHour := filter.EndTime.UTC().Truncate(time.Hour)
	start := currentHour.Add(-23 * time.Hour)
	return &start
}

const (
	usageOverviewHealthRows           = 7
	usageOverviewHealthDefaultColumns = 96
	usageOverviewHealthDefaultSpan    = 15 * time.Minute
	usageOverviewHealthPresetWindow   = 24 * time.Hour
	usageOverviewHealthPresetSpan     = (usageOverviewHealthPresetWindow + time.Duration(usageOverviewHealthRows*usageOverviewHealthDefaultColumns) - 1) / time.Duration(usageOverviewHealthRows*usageOverviewHealthDefaultColumns)
)

func buildUsageOverviewHealth(filter dto.UsageQueryFilter) dto.UsageOverviewHealthRecord {
	rows := usageOverviewHealthRows
	columns, span := usageOverviewHealthGrid(filter)
	totalBlocks := rows * columns
	windowStart, windowEnd := usageOverviewHealthWindow(filter, totalBlocks, span)
	blocks := make([]dto.UsageOverviewHealthBlockRecord, totalBlocks)
	for index := range blocks {
		startTime := windowStart.Add(time.Duration(index) * span)
		blocks[index] = dto.UsageOverviewHealthBlockRecord{
			StartTime: startTime,
			EndTime:   startTime.Add(span),
			Rate:      -1,
		}
	}
	return dto.UsageOverviewHealthRecord{
		Rows:          rows,
		Columns:       columns,
		BucketSeconds: int64((span + time.Second - 1) / time.Second),
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		BlockDetails:  blocks,
	}
}

func usageOverviewHealthGrid(filter dto.UsageQueryFilter) (int, time.Duration) {
	if isUsageOverviewShortHealthRange(filter.Range) {
		return usageOverviewHealthDefaultColumns, usageOverviewHealthPresetSpan
	}
	return usageOverviewHealthDefaultColumns, usageOverviewHealthDefaultSpan
}

func isUsageOverviewShortHealthRange(value string) bool {
	switch value {
	case "4h", "8h", "12h", "24h", "today":
		return true
	default:
		return false
	}
}

func usageOverviewHealthWindow(filter dto.UsageQueryFilter, totalBlocks int, span time.Duration) (time.Time, time.Time) {
	end := time.Now().UTC()
	if filter.EndTime != nil {
		end = filter.EndTime.UTC()
	}
	if isUsageOverviewShortHealthRange(filter.Range) {
		return end.Add(-usageOverviewHealthPresetWindow), end
	}
	currentBucketStart := end.Truncate(span)
	windowEnd := currentBucketStart.Add(span)
	return windowEnd.Add(-time.Duration(totalBlocks) * span), windowEnd
}

func updateUsageOverviewHealthBlock(blocks []dto.UsageOverviewHealthBlockRecord, event entities.UsageEvent) {
	if event.Failed {
		updateUsageOverviewHealthBlockCounts(blocks, event.Timestamp, 0, 1)
	} else {
		updateUsageOverviewHealthBlockCounts(blocks, event.Timestamp, 1, 0)
	}
}

func updateUsageOverviewHealthBlockWithAggregate(blocks []dto.UsageOverviewHealthBlockRecord, aggregate entities.UsageHourlyAggregate) {
	updateUsageOverviewHealthBlockCounts(blocks, aggregate.BucketStart, aggregate.SuccessCount, aggregate.FailureCount)
}

func updateUsageOverviewHealthBlockCounts(blocks []dto.UsageOverviewHealthBlockRecord, eventTime time.Time, success, failure int64) {
	if len(blocks) == 0 {
		return
	}
	windowStart := blocks[0].StartTime
	span := blocks[0].EndTime.Sub(windowStart)
	if span <= 0 {
		return
	}
	timestamp := eventTime.UTC()
	windowEnd := blocks[len(blocks)-1].EndTime
	if timestamp.Before(windowStart) || !timestamp.Before(windowEnd) {
		return
	}
	index := int(timestamp.Sub(windowStart) / span)
	if index < 0 || index >= len(blocks) {
		return
	}
	block := &blocks[index]
	if timestamp.Before(block.StartTime) || !timestamp.Before(block.EndTime) {
		return
	}
	block.Success += success
	block.Failure += failure
	total := block.Success + block.Failure
	if total > 0 {
		block.Rate = float64(block.Success) / float64(total)
	}
}
