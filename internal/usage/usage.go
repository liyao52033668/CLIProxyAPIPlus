package usage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"gorm.io/gorm"
)

const (
	maxInMemoryUsageEvents       = 1_000
	usageEventTrimBatch          = 100
	maxInMemoryUsageEventBytes   = int64(32 << 20)
	maxInMemoryUsageEventSize    = int64(16 << 10)
	maxInMemoryUsageEventAge     = 7 * 24 * time.Hour
	usageEventBaseSize           = int64(256)
	usageEventHealthRows         = 7
	usageEventHealthColumns      = 96
	usageEventHealthDefaultSpan  = 15 * time.Minute
	usageEventHealthPresetWindow = 24 * time.Hour
	usageEventHealthPresetSpan   = (usageEventHealthPresetWindow + time.Duration(usageEventHealthRows*usageEventHealthColumns) - 1) / time.Duration(usageEventHealthRows*usageEventHealthColumns)
)

var statisticsEnabled = true

func SetStatisticsEnabled(enabled bool) {
	statisticsEnabled = enabled
}

func IsStatisticsEnabled() bool {
	return statisticsEnabled
}

type DBConfig struct {
	Path      string
	AutoClean bool
}

func InitUsageDB(cfg DBConfig) (*gorm.DB, error) {
	return repository.OpenDatabase(config.Config{
		SQLitePath: cfg.Path,
	})
}

func InsertUsageEvents(db *gorm.DB, events []entities.UsageEvent) (int, int, error) {
	return repository.InsertUsageEvents(db, events)
}

type APISnapshot struct {
	DisplayName   string                    `json:"display_name,omitempty"`
	TotalRequests int64                     `json:"total_requests"`
	SuccessCount  int64                     `json:"success_count"`
	FailureCount  int64                     `json:"failure_count"`
	TotalTokens   int64                     `json:"total_tokens"`
	Models        map[string]*ModelSnapshot `json:"models"`
}

type ModelSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}

type StatisticsSnapshot struct {
	TotalRequests  int64                   `json:"total_requests"`
	SuccessCount   int64                   `json:"success_count"`
	FailureCount   int64                   `json:"failure_count"`
	TotalTokens    int64                   `json:"total_tokens"`
	APIs           map[string]*APISnapshot `json:"apis"`
	RequestsByDay  map[string]int64        `json:"requests_by_day"`
	RequestsByHour map[string]int64        `json:"requests_by_hour"`
	TokensByDay    map[string]int64        `json:"tokens_by_day"`
	TokensByHour   map[string]int64        `json:"tokens_by_hour"`
}

type MergeResult struct {
	Added   int
	Skipped int
}

type usageCredentialKey struct {
	source    string
	authIndex string
}

type RequestStatistics struct {
	mu                     sync.RWMutex
	apis                   map[string]*APISnapshot
	requestsByDay          map[string]int64
	requestsByHour         map[string]int64
	tokensByDay            map[string]int64
	tokensByHour           map[string]int64
	events                 []servicedto.UsageEventRecord
	eventBytes             int64
	nextEventID            uint
	hasOlderEvents         bool
	evictedEvents          int64
	oversizedDroppedEvents int64
}

var globalStats *RequestStatistics
var once sync.Once
var statisticsPluginOnce sync.Once

func GetRequestStatistics() *RequestStatistics {
	once.Do(func() {
		globalStats = NewRequestStatistics()
	})
	return globalStats
}

func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*APISnapshot),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[string]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[string]int64),
		nextEventID:    1,
	}
}

func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := StatisticsSnapshot{
		APIs:           make(map[string]*APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}

	for day, count := range s.requestsByDay {
		snapshot.RequestsByDay[day] = count
	}
	for hour, count := range s.requestsByHour {
		snapshot.RequestsByHour[hour] = count
	}
	for day, count := range s.tokensByDay {
		snapshot.TokensByDay[day] = count
	}
	for hour, count := range s.tokensByHour {
		snapshot.TokensByHour[hour] = count
	}

	for apiName, apiStats := range s.apis {
		apiSnapshot := &APISnapshot{
			DisplayName:   apiStats.DisplayName,
			TotalRequests: apiStats.TotalRequests,
			SuccessCount:  apiStats.SuccessCount,
			FailureCount:  apiStats.FailureCount,
			TotalTokens:   apiStats.TotalTokens,
			Models:        make(map[string]*ModelSnapshot),
		}
		for modelName, modelStats := range apiStats.Models {
			if modelStats != nil {
				apiSnapshot.Models[modelName] = &ModelSnapshot{
					TotalRequests: modelStats.TotalRequests,
					SuccessCount:  modelStats.SuccessCount,
					FailureCount:  modelStats.FailureCount,
					TotalTokens:   modelStats.TotalTokens,
					InputTokens:   modelStats.InputTokens,
					OutputTokens:  modelStats.OutputTokens,
				}
			}
		}
		snapshot.APIs[apiName] = apiSnapshot
		snapshot.TotalRequests += apiStats.TotalRequests
		snapshot.SuccessCount += apiStats.SuccessCount
		snapshot.FailureCount += apiStats.FailureCount
		snapshot.TotalTokens += apiStats.TotalTokens
	}

	return snapshot
}

func (s *RequestStatistics) Record(apiName, modelName string, timestamp time.Time, failed bool, inputTokens, outputTokens, totalTokens int64) {
	if s == nil {
		return
	}
	apiName = normalizeDimension(apiName)
	modelName = normalizeDimension(modelName)
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	apiStats := s.apis[apiName]
	if apiStats == nil {
		apiStats = &APISnapshot{DisplayName: apiName, Models: make(map[string]*ModelSnapshot)}
		s.apis[apiName] = apiStats
	}
	modelStats := apiStats.Models[modelName]
	if modelStats == nil {
		modelStats = &ModelSnapshot{}
		apiStats.Models[modelName] = modelStats
	}

	apiStats.TotalRequests++
	apiStats.TotalTokens += totalTokens
	modelStats.TotalRequests++
	modelStats.TotalTokens += totalTokens
	modelStats.InputTokens += inputTokens
	modelStats.OutputTokens += outputTokens
	if failed {
		apiStats.FailureCount++
		modelStats.FailureCount++
	} else {
		apiStats.SuccessCount++
		modelStats.SuccessCount++
	}

	dayKey := timestamp.In(time.Local).Format("2006-01-02")
	hourKey := timestamp.UTC().Format("2006-01-02T15:00:00Z")
	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func (s *RequestStatistics) RecordEvent(record coreusage.Record) {
	if s == nil {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens + record.Detail.CachedTokens
	}
	event := normalizeUsageEvent(servicedto.UsageEventRecord{
		Timestamp:       timestamp,
		APIGroupKey:     record.APIKey,
		Model:           record.Model,
		AuthType:        record.AuthType,
		Provider:        record.Provider,
		Source:          record.Source,
		AuthIndex:       record.AuthIndex,
		Failed:          record.Failed,
		LatencyMS:       record.Latency.Milliseconds(),
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     totalTokens,
	})
	eventSize := estimateUsageEventBytes(event)

	s.mu.Lock()
	defer s.mu.Unlock()
	if eventSize > maxInMemoryUsageEventSize {
		s.oversizedDroppedEvents++
		s.hasOlderEvents = true
		return
	}
	if event.Timestamp.Before(time.Now().UTC().Add(-maxInMemoryUsageEventAge)) {
		s.evictedEvents++
		s.hasOlderEvents = true
		return
	}
	if s.nextEventID == 0 {
		s.nextEventID = 1
	}
	event.ID = s.nextEventID
	s.nextEventID++
	if len(s.events) == 0 || !usageEventLess(event, s.events[len(s.events)-1]) {
		s.events = append(s.events, event)
	} else {
		index := sort.Search(len(s.events), func(i int) bool {
			return !usageEventLess(s.events[i], event)
		})
		s.events = append(s.events, servicedto.UsageEventRecord{})
		copy(s.events[index+1:], s.events[index:])
		s.events[index] = event
	}
	s.eventBytes += eventSize
	s.pruneUsageEventsLocked(time.Now().UTC())
}

func (s *RequestStatistics) ReplaceEvents(events []servicedto.UsageEventRecord) {
	if s == nil {
		return
	}
	replacement := append([]servicedto.UsageEventRecord(nil), events...)
	for i := range replacement {
		replacement[i] = normalizeUsageEvent(replacement[i])
	}
	sort.Slice(replacement, func(i, j int) bool {
		return usageEventLess(replacement[i], replacement[j])
	})
	nextEventID := uint(1)
	for _, event := range replacement {
		if event.ID >= nextEventID {
			nextEventID = event.ID + 1
		}
	}

	cutoff := time.Now().UTC().Add(-maxInMemoryUsageEventAge)
	retained := make([]servicedto.UsageEventRecord, 0, min(len(replacement), maxInMemoryUsageEvents))
	retainedBytes := int64(0)
	discarded := int64(0)
	oversized := int64(0)
	for i := len(replacement) - 1; i >= 0; i-- {
		event := replacement[i]
		if event.Timestamp.Before(cutoff) {
			discarded += int64(i + 1)
			break
		}
		eventSize := estimateUsageEventBytes(event)
		if eventSize > maxInMemoryUsageEventSize {
			oversized++
			continue
		}
		if len(retained) >= maxInMemoryUsageEvents || retainedBytes+eventSize > maxInMemoryUsageEventBytes {
			discarded += int64(i + 1)
			break
		}
		retained = append(retained, event)
		retainedBytes += eventSize
	}
	for left, right := 0, len(retained)-1; left < right; left, right = left+1, right-1 {
		retained[left], retained[right] = retained[right], retained[left]
	}

	s.mu.Lock()
	s.events = retained
	s.eventBytes = retainedBytes
	s.nextEventID = nextEventID
	s.hasOlderEvents = discarded > 0 || oversized > 0
	s.evictedEvents = discarded
	s.oversizedDroppedEvents = oversized
	s.mu.Unlock()
}

func (s *RequestStatistics) ListUsageEvents(filter servicedto.UsageFilter) *servicedto.UsageEventsPage {
	page, pageSize, offset := normalizeUsageEventPagination(filter)
	result := &servicedto.UsageEventsPage{
		Events:   []servicedto.UsageEventRecord{},
		Models:   []string{},
		Page:     page,
		PageSize: pageSize,
	}
	if s == nil {
		return result
	}

	events, cacheInfo := s.snapshotUsageEvents()
	result.Cache = &cacheInfo
	start, end := usageEventWindowBounds(events, filter)
	models := make(map[string]struct{})
	for i := end - 1; i >= start; i-- {
		event := events[i]
		if model := strings.TrimSpace(event.Model); model != "" {
			models[model] = struct{}{}
		}
		if !usageEventMatches(event, filter) {
			continue
		}
		if result.TotalCount >= int64(offset) && len(result.Events) < pageSize {
			result.Events = append(result.Events, event)
		}
		result.TotalCount++
	}
	for model := range models {
		result.Models = append(result.Models, model)
	}
	sort.Strings(result.Models)
	if result.TotalCount > 0 {
		result.TotalPages = int((result.TotalCount + int64(pageSize) - 1) / int64(pageSize))
	}
	return result
}

func (s *RequestStatistics) ListUsageEventFilterOptions(filter servicedto.UsageFilter) *servicedto.UsageEventFilterOptions {
	options := &servicedto.UsageEventFilterOptions{Models: []string{}}
	if s == nil {
		return options
	}

	events, _ := s.snapshotUsageEvents()
	start, end := usageEventWindowBounds(events, filter)
	models := make(map[string]struct{})
	for i := start; i < end; i++ {
		if model := strings.TrimSpace(events[i].Model); model != "" {
			models[model] = struct{}{}
		}
	}
	for model := range models {
		options.Models = append(options.Models, model)
	}
	sort.Strings(options.Models)
	return options
}

func (s *RequestStatistics) UsageEventOverview(filter servicedto.UsageFilter) (servicedto.UsageKeyStats, servicedto.UsageOverviewHealth, servicedto.UsageEventCacheInfo) {
	keyStats := servicedto.UsageKeyStats{
		BySource:    make(map[string]servicedto.UsageKeyCount),
		ByAuthIndex: make(map[string]servicedto.UsageKeyCount),
		Credentials: []servicedto.UsageCredentialCount{},
	}
	health := newUsageEventHealth(filter, time.Now().UTC())
	if s == nil {
		return keyStats, health, servicedto.UsageEventCacheInfo{
			MaxEvents:     maxInMemoryUsageEvents,
			MaxBytes:      maxInMemoryUsageEventBytes,
			MaxAgeSeconds: int64(maxInMemoryUsageEventAge / time.Second),
			MaxEventBytes: maxInMemoryUsageEventSize,
		}
	}

	events, cacheInfo := s.snapshotUsageEvents()
	start, end := usageEventWindowBounds(events, filter)
	credentials := make(map[usageCredentialKey]servicedto.UsageKeyCount)
	for i := start; i < end; i++ {
		event := events[i]
		updateUsageEventKeyStats(&keyStats, credentials, event)
		updateUsageEventHealth(&health, event)
	}
	credentialKeys := make([]usageCredentialKey, 0, len(credentials))
	for key := range credentials {
		credentialKeys = append(credentialKeys, key)
	}
	sort.Slice(credentialKeys, func(i, j int) bool {
		if credentialKeys[i].source == credentialKeys[j].source {
			return credentialKeys[i].authIndex < credentialKeys[j].authIndex
		}
		return credentialKeys[i].source < credentialKeys[j].source
	})
	for _, key := range credentialKeys {
		count := credentials[key]
		keyStats.Credentials = append(keyStats.Credentials, servicedto.UsageCredentialCount{
			Source:    key.source,
			AuthIndex: key.authIndex,
			Success:   count.Success,
			Failure:   count.Failure,
			Tokens:    count.Tokens,
			Cost:      count.Cost,
		})
	}
	if total := health.TotalSuccess + health.TotalFailure; total > 0 {
		health.SuccessRate = float64(health.TotalSuccess) / float64(total) * 100
	}
	return keyStats, health, cacheInfo
}

// UsageEventCacheInfo returns the current bounded event cache state.
func (s *RequestStatistics) UsageEventCacheInfo() servicedto.UsageEventCacheInfo {
	if s == nil {
		return servicedto.UsageEventCacheInfo{
			MaxEvents:     maxInMemoryUsageEvents,
			MaxBytes:      maxInMemoryUsageEventBytes,
			MaxAgeSeconds: int64(maxInMemoryUsageEventAge / time.Second),
			MaxEventBytes: maxInMemoryUsageEventSize,
		}
	}
	s.mu.Lock()
	s.pruneUsageEventsLocked(time.Now().UTC())
	info := s.usageEventCacheInfoLocked()
	s.mu.Unlock()
	return info
}

func (s *RequestStatistics) snapshotUsageEvents() ([]servicedto.UsageEventRecord, servicedto.UsageEventCacheInfo) {
	s.mu.Lock()
	s.pruneUsageEventsLocked(time.Now().UTC())
	events := append([]servicedto.UsageEventRecord(nil), s.events...)
	cacheInfo := s.usageEventCacheInfoLocked()
	s.mu.Unlock()
	return events, cacheInfo
}

func (s *RequestStatistics) pruneUsageEventsLocked(now time.Time) {
	if len(s.events) == 0 {
		s.eventBytes = 0
		return
	}
	start := sort.Search(len(s.events), func(i int) bool {
		return !s.events[i].Timestamp.Before(now.Add(-maxInMemoryUsageEventAge))
	})
	remainingCount := len(s.events) - start
	if remainingCount > maxInMemoryUsageEvents {
		keepCount := maxInMemoryUsageEvents - usageEventTrimBatch
		start = max(start, len(s.events)-keepCount)
	}
	remainingBytes := s.eventBytes
	for i := 0; i < start; i++ {
		remainingBytes -= estimateUsageEventBytes(s.events[i])
	}
	if remainingBytes > maxInMemoryUsageEventBytes {
		targetBytes := maxInMemoryUsageEventBytes - maxInMemoryUsageEventBytes/10
		for start < len(s.events) && remainingBytes > targetBytes {
			remainingBytes -= estimateUsageEventBytes(s.events[start])
			start++
		}
	}
	if start == 0 {
		return
	}
	retained := append([]servicedto.UsageEventRecord(nil), s.events[start:]...)
	s.events = retained
	s.eventBytes = max(remainingBytes, 0)
	s.hasOlderEvents = true
	s.evictedEvents += int64(start)
}

func (s *RequestStatistics) usageEventCacheInfoLocked() servicedto.UsageEventCacheInfo {
	info := servicedto.UsageEventCacheInfo{
		RetainedCount:         len(s.events),
		MaxEvents:             maxInMemoryUsageEvents,
		EstimatedBytes:        s.eventBytes,
		MaxBytes:              maxInMemoryUsageEventBytes,
		MaxAgeSeconds:         int64(maxInMemoryUsageEventAge / time.Second),
		MaxEventBytes:         maxInMemoryUsageEventSize,
		HasOlderEvents:        s.hasOlderEvents,
		EvictedTotal:          s.evictedEvents,
		OversizedDroppedTotal: s.oversizedDroppedEvents,
	}
	if len(s.events) > 0 {
		oldest := s.events[0].Timestamp.UTC()
		newest := s.events[len(s.events)-1].Timestamp.UTC()
		info.OldestTimestamp = &oldest
		info.NewestTimestamp = &newest
	}
	return info
}

func updateUsageEventKeyStats(stats *servicedto.UsageKeyStats, credentials map[usageCredentialKey]servicedto.UsageKeyCount, event servicedto.UsageEventRecord) {
	if stats == nil {
		return
	}
	source := strings.TrimSpace(event.Source)
	authIndex := strings.TrimSpace(event.AuthIndex)
	apply := func(count servicedto.UsageKeyCount) servicedto.UsageKeyCount {
		if event.Failed {
			count.Failure++
		} else {
			count.Success++
		}
		count.Tokens += event.TotalTokens
		return count
	}
	if source != "" {
		stats.BySource[source] = apply(stats.BySource[source])
	}
	if authIndex != "" {
		stats.ByAuthIndex[authIndex] = apply(stats.ByAuthIndex[authIndex])
	}
	if source != "" || authIndex != "" {
		key := usageCredentialKey{source: source, authIndex: authIndex}
		credentials[key] = apply(credentials[key])
	}
}

func newUsageEventHealth(filter servicedto.UsageFilter, now time.Time) servicedto.UsageOverviewHealth {
	span := usageEventHealthDefaultSpan
	shortRange := isShortUsageEventHealthRange(filter.Range)
	if shortRange {
		span = usageEventHealthPresetSpan
	}
	windowEnd := now.UTC()
	if filter.EndTime != nil {
		windowEnd = filter.EndTime.UTC()
	}
	if shortRange {
		windowStart := windowEnd.Add(-usageEventHealthPresetWindow)
		return buildUsageEventHealthBlocks(windowStart, windowEnd, span)
	}
	windowEnd = windowEnd.Truncate(span).Add(span)
	windowStart := windowEnd.Add(-time.Duration(usageEventHealthRows*usageEventHealthColumns) * span)
	return buildUsageEventHealthBlocks(windowStart, windowEnd, span)
}

func buildUsageEventHealthBlocks(windowStart, windowEnd time.Time, span time.Duration) servicedto.UsageOverviewHealth {
	blocks := make([]servicedto.UsageOverviewHealthBlock, usageEventHealthRows*usageEventHealthColumns)
	for i := range blocks {
		startTime := windowStart.Add(time.Duration(i) * span)
		blocks[i] = servicedto.UsageOverviewHealthBlock{
			StartTime: startTime,
			EndTime:   startTime.Add(span),
			Rate:      -1,
		}
	}
	return servicedto.UsageOverviewHealth{
		Rows:          usageEventHealthRows,
		Columns:       usageEventHealthColumns,
		BucketSeconds: int64((span + time.Second - 1) / time.Second),
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		BlockDetails:  blocks,
	}
}

func updateUsageEventHealth(health *servicedto.UsageOverviewHealth, event servicedto.UsageEventRecord) {
	if health == nil {
		return
	}
	if event.Failed {
		health.TotalFailure++
	} else {
		health.TotalSuccess++
	}
	if len(health.BlockDetails) == 0 {
		return
	}
	timestamp := event.Timestamp.UTC()
	if timestamp.Before(health.WindowStart) || !timestamp.Before(health.WindowEnd) {
		return
	}
	span := health.BlockDetails[0].EndTime.Sub(health.BlockDetails[0].StartTime)
	if span <= 0 {
		return
	}
	index := int(timestamp.Sub(health.WindowStart) / span)
	if index < 0 || index >= len(health.BlockDetails) {
		return
	}
	block := &health.BlockDetails[index]
	if event.Failed {
		block.Failure++
	} else {
		block.Success++
	}
	total := block.Success + block.Failure
	block.Rate = float64(block.Success) / float64(total)
}

func isShortUsageEventHealthRange(value string) bool {
	switch value {
	case "4h", "8h", "12h", "24h", "today":
		return true
	default:
		return false
	}
}

func estimateUsageEventBytes(event servicedto.UsageEventRecord) int64 {
	return usageEventBaseSize + int64(
		len(event.APIGroupKey)+len(event.Model)+len(event.AuthType)+len(event.Provider)+len(event.Source)+len(event.AuthIndex),
	)
}

func normalizeUsageEventPagination(filter servicedto.UsageFilter) (page, pageSize, offset int) {
	page = filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize = filter.PageSize
	if pageSize <= 0 {
		pageSize = filter.Limit
	}
	if pageSize <= 0 {
		pageSize = servicedto.DefaultUsageEventsLimit
	}
	offset = filter.Offset
	if offset <= 0 {
		offset = (page - 1) * pageSize
	}
	if offset < 0 {
		offset = 0
	}
	return page, pageSize, offset
}

func usageEventWindowBounds(events []servicedto.UsageEventRecord, filter servicedto.UsageFilter) (int, int) {
	start := 0
	if filter.StartTime != nil {
		startTime := filter.StartTime.UTC()
		start = sort.Search(len(events), func(i int) bool {
			return !events[i].Timestamp.Before(startTime)
		})
	}
	end := len(events)
	if filter.EndTime != nil {
		endTime := filter.EndTime.UTC()
		end = sort.Search(len(events), func(i int) bool {
			return events[i].Timestamp.After(endTime)
		})
	}
	if end < start {
		end = start
	}
	return start, end
}

func usageEventMatches(event servicedto.UsageEventRecord, filter servicedto.UsageFilter) bool {
	if model := strings.TrimSpace(filter.Model); model != "" && strings.TrimSpace(event.Model) != model {
		return false
	}
	source := strings.TrimSpace(filter.Source)
	authIndex := strings.TrimSpace(filter.AuthIndex)
	switch {
	case source != "" && authIndex != "":
		if strings.TrimSpace(event.AuthIndex) != authIndex && strings.TrimSpace(event.Source) != source {
			return false
		}
	case source != "":
		if strings.TrimSpace(event.Source) != source {
			return false
		}
	case authIndex != "":
		if strings.TrimSpace(event.AuthIndex) != authIndex {
			return false
		}
	}
	switch strings.TrimSpace(filter.Result) {
	case "success":
		return !event.Failed
	case "failed":
		return event.Failed
	default:
		return true
	}
}

func usageEventLess(left, right servicedto.UsageEventRecord) bool {
	if left.Timestamp.Equal(right.Timestamp) {
		return left.ID < right.ID
	}
	return left.Timestamp.Before(right.Timestamp)
}

func normalizeUsageEvent(event servicedto.UsageEventRecord) servicedto.UsageEventRecord {
	event.Timestamp = event.Timestamp.UTC()
	event.APIGroupKey = strings.Clone(strings.TrimSpace(event.APIGroupKey))
	event.Model = strings.Clone(strings.TrimSpace(event.Model))
	event.AuthType = strings.Clone(strings.TrimSpace(event.AuthType))
	event.Provider = strings.Clone(strings.TrimSpace(event.Provider))
	event.Source = strings.Clone(strings.TrimSpace(event.Source))
	event.AuthIndex = strings.Clone(strings.TrimSpace(event.AuthIndex))
	return event
}

func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()

	result := MergeResult{}

	for apiName, apiStats := range snapshot.APIs {
		if apiStats == nil {
			continue
		}
		if _, ok := s.apis[apiName]; !ok {
			s.apis[apiName] = &APISnapshot{
				Models: make(map[string]*ModelSnapshot),
			}
		}
		if s.apis[apiName].Models == nil {
			s.apis[apiName].Models = make(map[string]*ModelSnapshot)
		}

		s.apis[apiName].DisplayName = apiStats.DisplayName
		s.apis[apiName].TotalRequests += apiStats.TotalRequests
		s.apis[apiName].SuccessCount += apiStats.SuccessCount
		s.apis[apiName].FailureCount += apiStats.FailureCount
		s.apis[apiName].TotalTokens += apiStats.TotalTokens

		for modelName, modelStats := range apiStats.Models {
			if modelStats == nil {
				continue
			}
			if _, ok := s.apis[apiName].Models[modelName]; !ok {
				s.apis[apiName].Models[modelName] = &ModelSnapshot{}
				result.Added++
			} else {
				result.Skipped++
			}

			s.apis[apiName].Models[modelName].TotalRequests += modelStats.TotalRequests
			s.apis[apiName].Models[modelName].SuccessCount += modelStats.SuccessCount
			s.apis[apiName].Models[modelName].FailureCount += modelStats.FailureCount
			s.apis[apiName].Models[modelName].TotalTokens += modelStats.TotalTokens
			s.apis[apiName].Models[modelName].InputTokens += modelStats.InputTokens
			s.apis[apiName].Models[modelName].OutputTokens += modelStats.OutputTokens
		}
	}
	for day, count := range snapshot.RequestsByDay {
		s.requestsByDay[day] += count
	}
	for hour, count := range snapshot.RequestsByHour {
		s.requestsByHour[hour] += count
	}
	for day, count := range snapshot.TokensByDay {
		s.tokensByDay[day] += count
	}
	for hour, count := range snapshot.TokensByHour {
		s.tokensByHour[hour] += count
	}

	return result
}

func (s *RequestStatistics) ReplaceSnapshot(snapshot StatisticsSnapshot) {
	if s == nil {
		return
	}
	replacement := NewRequestStatistics()
	replacement.MergeSnapshot(snapshot)

	s.mu.Lock()
	s.apis = replacement.apis
	s.requestsByDay = replacement.requestsByDay
	s.requestsByHour = replacement.requestsByHour
	s.tokensByDay = replacement.tokensByDay
	s.tokensByHour = replacement.tokensByHour
	s.mu.Unlock()
}

func (s *RequestStatistics) ReplaceAll(snapshot StatisticsSnapshot, events []servicedto.UsageEventRecord) {
	if s == nil {
		return
	}
	replacement := NewRequestStatistics()
	replacement.MergeSnapshot(snapshot)
	replacement.ReplaceEvents(events)

	s.mu.Lock()
	s.apis = replacement.apis
	s.requestsByDay = replacement.requestsByDay
	s.requestsByHour = replacement.requestsByHour
	s.tokensByDay = replacement.tokensByDay
	s.tokensByHour = replacement.tokensByHour
	s.events = replacement.events
	s.eventBytes = replacement.eventBytes
	s.nextEventID = replacement.nextEventID
	s.hasOlderEvents = replacement.hasOlderEvents
	s.evictedEvents = replacement.evictedEvents
	s.oversizedDroppedEvents = replacement.oversizedDroppedEvents
	s.mu.Unlock()
}

func (s *RequestStatistics) HasData() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.apis) > 0 || len(s.events) > 0
}

func (s *RequestStatistics) ensureMaps() {
	if s.apis == nil {
		s.apis = make(map[string]*APISnapshot)
	}
	if s.requestsByDay == nil {
		s.requestsByDay = make(map[string]int64)
	}
	if s.requestsByHour == nil {
		s.requestsByHour = make(map[string]int64)
	}
	if s.tokensByDay == nil {
		s.tokensByDay = make(map[string]int64)
	}
	if s.tokensByHour == nil {
		s.tokensByHour = make(map[string]int64)
	}
}

func normalizeDimension(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func RestoreRequestStatistics(ctx context.Context, db *gorm.DB, stats *RequestStatistics) error {
	if stats == nil {
		stats = GetRequestStatistics()
	}
	if stats.HasData() {
		return nil
	}
	if db == nil {
		return fmt.Errorf("restore request statistics: database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider := service.NewUsageService(db.WithContext(ctx))
	if err := ReloadRequestStatistics(ctx, provider, stats); err != nil {
		return fmt.Errorf("restore request statistics: %w", err)
	}
	return nil
}

func ReloadRequestStatistics(ctx context.Context, provider service.UsageProvider, stats *RequestStatistics) error {
	if provider == nil {
		return fmt.Errorf("usage provider is nil")
	}
	if stats == nil {
		stats = GetRequestStatistics()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	aggregate, err := provider.GetUsageAggregateWithFilter(ctx, servicedto.UsageFilter{})
	if err != nil {
		return fmt.Errorf("load usage aggregate: %w", err)
	}
	analysis, err := provider.GetUsageAnalysis(ctx, servicedto.UsageFilter{})
	if err != nil {
		return fmt.Errorf("load usage analysis: %w", err)
	}
	eventsPage, err := provider.ListUsageEvents(ctx, servicedto.UsageFilter{
		Page:     1,
		PageSize: maxInMemoryUsageEvents + 1,
	})
	if err != nil {
		return fmt.Errorf("load usage events: %w", err)
	}

	snapshot := StatisticsSnapshot{
		TotalRequests:  aggregate.TotalRequests,
		SuccessCount:   aggregate.SuccessCount,
		FailureCount:   aggregate.FailureCount,
		TotalTokens:    aggregate.TotalTokens,
		APIs:           make(map[string]*APISnapshot, len(aggregate.APIs)),
		RequestsByDay:  aggregate.RequestsByDay,
		RequestsByHour: aggregate.RequestsByHour,
		TokensByDay:    aggregate.TokensByDay,
		TokensByHour:   aggregate.TokensByHour,
	}
	for apiKey, apiStats := range aggregate.APIs {
		models := make(map[string]*ModelSnapshot, len(apiStats.Models))
		for modelName, modelStats := range apiStats.Models {
			models[modelName] = &ModelSnapshot{
				TotalRequests: modelStats.TotalRequests,
				SuccessCount:  modelStats.SuccessCount,
				FailureCount:  modelStats.FailureCount,
				TotalTokens:   modelStats.TotalTokens,
			}
		}
		snapshot.APIs[apiKey] = &APISnapshot{
			DisplayName:   apiStats.DisplayName,
			TotalRequests: apiStats.TotalRequests,
			SuccessCount:  apiStats.SuccessCount,
			FailureCount:  apiStats.FailureCount,
			TotalTokens:   apiStats.TotalTokens,
			Models:        models,
		}
	}
	for _, apiStats := range analysis.APIs {
		apiKey := normalizeDimension(apiStats.APIKey)
		apiSnapshot := snapshot.APIs[apiKey]
		if apiSnapshot == nil {
			apiSnapshot = &APISnapshot{Models: make(map[string]*ModelSnapshot)}
			snapshot.APIs[apiKey] = apiSnapshot
		}
		apiSnapshot.DisplayName = apiStats.DisplayName
		if apiSnapshot.DisplayName == "" {
			apiSnapshot.DisplayName = apiKey
		}
		for _, modelStats := range apiStats.Models {
			modelName := normalizeDimension(modelStats.Model)
			modelSnapshot := apiSnapshot.Models[modelName]
			if modelSnapshot == nil {
				modelSnapshot = &ModelSnapshot{
					TotalRequests: modelStats.TotalRequests,
					SuccessCount:  modelStats.SuccessCount,
					FailureCount:  modelStats.FailureCount,
					TotalTokens:   modelStats.TotalTokens,
				}
				apiSnapshot.Models[modelName] = modelSnapshot
			}
			modelSnapshot.InputTokens = modelStats.InputTokens
			modelSnapshot.OutputTokens = modelStats.OutputTokens
		}
	}
	stats.ReplaceAll(snapshot, eventsPage.Events)
	return nil
}

type requestStatisticsPlugin struct {
	stats *RequestStatistics
}

func (p *requestStatisticsPlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil || p.stats == nil || !IsStatisticsEnabled() {
		return
	}
	totalTokens := record.Detail.TotalTokens
	if totalTokens == 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens + record.Detail.CachedTokens
	}
	p.stats.Record(record.APIKey, record.Model, record.RequestedAt, record.Failed, record.Detail.InputTokens, record.Detail.OutputTokens, totalTokens)
	p.stats.RecordEvent(record)
}

func RegisterRequestStatisticsPlugin() {
	statisticsPluginOnce.Do(func() {
		coreusage.RegisterPlugin(&requestStatisticsPlugin{stats: GetRequestStatistics()})
	})
}

type UsagePersister interface {
	Persist(snapshot StatisticsSnapshot) error
}

type dbUsagePersister struct {
	db *gorm.DB
}

func NewDBUsagePersister(db *gorm.DB) UsagePersister {
	return &dbUsagePersister{db: db}
}

func (p *dbUsagePersister) Persist(snapshot StatisticsSnapshot) error {
	return nil
}

func StartPersistence(persister UsagePersister, interval time.Duration) {
}

func BuildEventKey(apiKey, model string, timestamp time.Time, source, authIndex string, failed bool, tokens dto.TokenStats) string {
	return service.BuildEventKey(apiKey, model, timestamp, source, authIndex, failed, tokens)
}

func GetUsageService(db *gorm.DB) service.UsageProvider {
	return service.NewUsageService(db)
}

func GetSyncService(db *gorm.DB, cfg config.Config) *service.SyncService {
	return service.NewSyncService(db, cfg)
}

func GetPricingService(db *gorm.DB, baseURL, managementKey string, timeout time.Duration, tlsSkipVerify bool) service.PricingProvider {
	return service.NewPricingService(db, cpa.NewClient(baseURL, managementKey, timeout, tlsSkipVerify))
}

func CleanupStorage(db *gorm.DB) error {
	_, err := repository.CleanupStorage(db, time.Now())
	return err
}
