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
	recentUsageRateWindow        = 30 * time.Minute
	recentUsageMinuteRetention   = 2 * time.Hour
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

type UsageBucketSnapshot struct {
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

type ModelSnapshot struct {
	TotalRequests      int64                          `json:"total_requests"`
	SuccessCount       int64                          `json:"success_count"`
	FailureCount       int64                          `json:"failure_count"`
	TotalTokens        int64                          `json:"total_tokens"`
	InputTokens        int64                          `json:"input_tokens"`
	OutputTokens       int64                          `json:"output_tokens"`
	ReasoningTokens    int64                          `json:"reasoning_tokens"`
	CachedTokens       int64                          `json:"cached_tokens"`
	TotalLatencyMS     int64                          `json:"total_latency_ms"`
	LatencySampleCount int64                          `json:"latency_sample_count"`
	Hourly             map[string]UsageBucketSnapshot `json:"-"`
}

type UsageOverviewSnapshot struct {
	Usage        StatisticsSnapshot
	Summary      servicedto.UsageOverviewSummary
	Series       servicedto.UsageOverviewSeries
	HourlySeries servicedto.UsageOverviewSeries
	DailySeries  servicedto.UsageOverviewSeries
	StartTime    *time.Time
	EndTime      *time.Time
	BucketByDay  bool
}

type StatisticsSnapshot struct {
	TotalRequests    int64                                                 `json:"total_requests"`
	SuccessCount     int64                                                 `json:"success_count"`
	FailureCount     int64                                                 `json:"failure_count"`
	TotalTokens      int64                                                 `json:"total_tokens"`
	APIs             map[string]*APISnapshot                               `json:"apis"`
	RequestsByDay    map[string]int64                                      `json:"requests_by_day"`
	RequestsByHour   map[string]int64                                      `json:"requests_by_hour"`
	TokensByDay      map[string]int64                                      `json:"tokens_by_day"`
	TokensByHour     map[string]int64                                      `json:"tokens_by_hour"`
	CredentialHourly map[string]map[usageCredentialKey]UsageBucketSnapshot `json:"-"`
	MinuteBuckets    map[string]UsageBucketSnapshot                        `json:"-"`
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
	credentialHourly       map[string]map[usageCredentialKey]UsageBucketSnapshot
	minuteBuckets          map[string]UsageBucketSnapshot
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
		apis:             make(map[string]*APISnapshot),
		requestsByDay:    make(map[string]int64),
		requestsByHour:   make(map[string]int64),
		tokensByDay:      make(map[string]int64),
		tokensByHour:     make(map[string]int64),
		credentialHourly: make(map[string]map[usageCredentialKey]UsageBucketSnapshot),
		minuteBuckets:    make(map[string]UsageBucketSnapshot),
		nextEventID:      1,
	}
}

func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := StatisticsSnapshot{
		APIs:             make(map[string]*APISnapshot),
		RequestsByDay:    make(map[string]int64),
		RequestsByHour:   make(map[string]int64),
		TokensByDay:      make(map[string]int64),
		TokensByHour:     make(map[string]int64),
		CredentialHourly: cloneCredentialBuckets(s.credentialHourly),
		MinuteBuckets:    cloneUsageBuckets(s.minuteBuckets),
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
					TotalRequests:      modelStats.TotalRequests,
					SuccessCount:       modelStats.SuccessCount,
					FailureCount:       modelStats.FailureCount,
					TotalTokens:        modelStats.TotalTokens,
					InputTokens:        modelStats.InputTokens,
					OutputTokens:       modelStats.OutputTokens,
					ReasoningTokens:    modelStats.ReasoningTokens,
					CachedTokens:       modelStats.CachedTokens,
					TotalLatencyMS:     modelStats.TotalLatencyMS,
					LatencySampleCount: modelStats.LatencySampleCount,
					Hourly:             cloneUsageBuckets(modelStats.Hourly),
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
	s.record(apiName, modelName, "", "", timestamp, failed, inputTokens, outputTokens, 0, 0, totalTokens, 0)
}

func (s *RequestStatistics) record(apiName, modelName, source, authIndex string, timestamp time.Time, failed bool, inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens, latencyMS int64) {
	if s == nil {
		return
	}
	apiName = normalizeDimension(apiName)
	modelName = normalizeDimension(modelName)
	source = strings.TrimSpace(source)
	authIndex = strings.TrimSpace(authIndex)
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens + reasoningTokens + cachedTokens
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
		modelStats = &ModelSnapshot{Hourly: make(map[string]UsageBucketSnapshot)}
		apiStats.Models[modelName] = modelStats
	}
	if modelStats.Hourly == nil {
		modelStats.Hourly = make(map[string]UsageBucketSnapshot)
	}

	apiStats.TotalRequests++
	apiStats.TotalTokens += totalTokens
	modelStats.TotalRequests++
	modelStats.TotalTokens += totalTokens
	modelStats.InputTokens += inputTokens
	modelStats.OutputTokens += outputTokens
	modelStats.ReasoningTokens += reasoningTokens
	modelStats.CachedTokens += cachedTokens
	if latencyMS > 0 {
		modelStats.TotalLatencyMS += latencyMS
		modelStats.LatencySampleCount++
	}
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
	bucket := modelStats.Hourly[hourKey]
	bucket.TotalRequests++
	bucket.InputTokens += inputTokens
	bucket.OutputTokens += outputTokens
	bucket.ReasoningTokens += reasoningTokens
	bucket.CachedTokens += cachedTokens
	bucket.TotalTokens += totalTokens
	if latencyMS > 0 {
		bucket.TotalLatencyMS += latencyMS
		bucket.LatencySampleCount++
	}
	if failed {
		bucket.FailureCount++
	} else {
		bucket.SuccessCount++
	}
	modelStats.Hourly[hourKey] = bucket

	if source != "" || authIndex != "" {
		credentialBuckets := s.credentialHourly[hourKey]
		if credentialBuckets == nil {
			credentialBuckets = make(map[usageCredentialKey]UsageBucketSnapshot)
		}
		credentialKey := usageCredentialKey{source: source, authIndex: authIndex}
		credentialBuckets[credentialKey] = mergeUsageBucket(credentialBuckets[credentialKey], UsageBucketSnapshot{
			TotalRequests: 1,
			SuccessCount:  boolCount(!failed),
			FailureCount:  boolCount(failed),
			TotalTokens:   totalTokens,
		})
		s.credentialHourly[hourKey] = credentialBuckets
	}

	minuteKey := timestamp.UTC().Truncate(time.Minute).Format(time.RFC3339)
	s.minuteBuckets[minuteKey] = mergeUsageBucket(s.minuteBuckets[minuteKey], UsageBucketSnapshot{
		TotalRequests:   1,
		SuccessCount:    boolCount(!failed),
		FailureCount:    boolCount(failed),
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		TotalTokens:     totalTokens,
	})
	s.pruneMinuteBucketsLocked(time.Now().UTC())
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
	if s == nil {
		return keyStats, buildUsageAggregateHealth(StatisticsSnapshot{}, filter, time.Now().UTC()), servicedto.UsageEventCacheInfo{
			MaxEvents:     maxInMemoryUsageEvents,
			MaxBytes:      maxInMemoryUsageEventBytes,
			MaxAgeSeconds: int64(maxInMemoryUsageEventAge / time.Second),
			MaxEventBytes: maxInMemoryUsageEventSize,
		}
	}

	snapshot := s.Snapshot()
	cacheInfo := s.UsageEventCacheInfo()
	credentials := make(map[usageCredentialKey]servicedto.UsageKeyCount)
	for hourKey, buckets := range snapshot.CredentialHourly {
		hour, err := time.Parse(time.RFC3339, hourKey)
		if err != nil || !memoryUsageHourInRange(hour, filter) {
			continue
		}
		for key, bucket := range buckets {
			count := servicedto.UsageKeyCount{
				Success: bucket.SuccessCount,
				Failure: bucket.FailureCount,
				Tokens:  bucket.TotalTokens,
			}
			if key.source != "" {
				keyStats.BySource[key.source] = mergeUsageKeyCount(keyStats.BySource[key.source], count)
			}
			if key.authIndex != "" {
				keyStats.ByAuthIndex[key.authIndex] = mergeUsageKeyCount(keyStats.ByAuthIndex[key.authIndex], count)
			}
			credentials[key] = mergeUsageKeyCount(credentials[key], count)
		}
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
	return keyStats, buildUsageAggregateHealth(snapshot, filter, time.Now().UTC()), cacheInfo
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

func mergeUsageKeyCount(left, right servicedto.UsageKeyCount) servicedto.UsageKeyCount {
	left.Success += right.Success
	left.Failure += right.Failure
	left.Tokens += right.Tokens
	left.Cost += right.Cost
	return left
}

func buildUsageAggregateHealth(snapshot StatisticsSnapshot, filter servicedto.UsageFilter, now time.Time) servicedto.UsageOverviewHealth {
	windowStart, windowEnd := usageAggregateHealthBounds(snapshot, filter, now)
	blockCount := usageEventHealthRows * usageEventHealthColumns
	span := (windowEnd.Sub(windowStart) + time.Duration(blockCount) - 1) / time.Duration(blockCount)
	if span <= 0 {
		span = time.Second
	}
	health := buildUsageEventHealthBlocks(windowStart, windowStart.Add(time.Duration(blockCount)*span), span)
	for _, apiStats := range snapshot.APIs {
		if apiStats == nil {
			continue
		}
		for _, modelStats := range apiStats.Models {
			if modelStats == nil {
				continue
			}
			for hourKey, bucket := range modelStats.Hourly {
				hour, err := time.Parse(time.RFC3339, hourKey)
				if err != nil || !memoryUsageHourInRange(hour, filter) {
					continue
				}
				updateUsageBucketHealth(&health, hour, bucket)
			}
		}
	}
	if total := health.TotalSuccess + health.TotalFailure; total > 0 {
		health.SuccessRate = float64(health.TotalSuccess) / float64(total) * 100
	}
	return health
}

func usageAggregateHealthBounds(snapshot StatisticsSnapshot, filter servicedto.UsageFilter, now time.Time) (time.Time, time.Time) {
	if filter.StartTime != nil && filter.EndTime != nil {
		return filter.StartTime.UTC(), filter.EndTime.UTC()
	}
	var earliest time.Time
	var latest time.Time
	for _, apiStats := range snapshot.APIs {
		if apiStats == nil {
			continue
		}
		for _, modelStats := range apiStats.Models {
			if modelStats == nil {
				continue
			}
			for hourKey := range modelStats.Hourly {
				hour, err := time.Parse(time.RFC3339, hourKey)
				if err != nil {
					continue
				}
				if earliest.IsZero() || hour.Before(earliest) {
					earliest = hour
				}
				if latest.IsZero() || hour.After(latest) {
					latest = hour
				}
			}
		}
	}
	if earliest.IsZero() || latest.IsZero() {
		return now.UTC().Add(-usageEventHealthPresetWindow), now.UTC()
	}
	return earliest.UTC(), latest.UTC().Add(time.Hour)
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

func updateUsageBucketHealth(health *servicedto.UsageOverviewHealth, timestamp time.Time, bucket UsageBucketSnapshot) {
	if health == nil {
		return
	}
	health.TotalSuccess += bucket.SuccessCount
	health.TotalFailure += bucket.FailureCount
	if len(health.BlockDetails) == 0 {
		return
	}
	timestamp = timestamp.UTC()
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
	block.Success += bucket.SuccessCount
	block.Failure += bucket.FailureCount
	total := block.Success + block.Failure
	block.Rate = float64(block.Success) / float64(total)
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

			targetModel := s.apis[apiName].Models[modelName]
			targetModel.TotalRequests += modelStats.TotalRequests
			targetModel.SuccessCount += modelStats.SuccessCount
			targetModel.FailureCount += modelStats.FailureCount
			targetModel.TotalTokens += modelStats.TotalTokens
			targetModel.InputTokens += modelStats.InputTokens
			targetModel.OutputTokens += modelStats.OutputTokens
			targetModel.ReasoningTokens += modelStats.ReasoningTokens
			targetModel.CachedTokens += modelStats.CachedTokens
			targetModel.TotalLatencyMS += modelStats.TotalLatencyMS
			targetModel.LatencySampleCount += modelStats.LatencySampleCount
			if targetModel.Hourly == nil {
				targetModel.Hourly = make(map[string]UsageBucketSnapshot)
			}
			for hour, bucket := range modelStats.Hourly {
				targetModel.Hourly[hour] = mergeUsageBucket(targetModel.Hourly[hour], bucket)
			}
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
	for hour, buckets := range snapshot.CredentialHourly {
		target := s.credentialHourly[hour]
		if target == nil {
			target = make(map[usageCredentialKey]UsageBucketSnapshot)
		}
		for key, bucket := range buckets {
			target[key] = mergeUsageBucket(target[key], bucket)
		}
		s.credentialHourly[hour] = target
	}
	for minute, bucket := range snapshot.MinuteBuckets {
		s.minuteBuckets[minute] = mergeUsageBucket(s.minuteBuckets[minute], bucket)
	}
	s.pruneMinuteBucketsLocked(time.Now().UTC())

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
	s.credentialHourly = replacement.credentialHourly
	s.minuteBuckets = replacement.minuteBuckets
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
	s.credentialHourly = replacement.credentialHourly
	s.minuteBuckets = replacement.minuteBuckets
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
	if s.credentialHourly == nil {
		s.credentialHourly = make(map[string]map[usageCredentialKey]UsageBucketSnapshot)
	}
	if s.minuteBuckets == nil {
		s.minuteBuckets = make(map[string]UsageBucketSnapshot)
	}
}

func normalizeDimension(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func cloneUsageBuckets(source map[string]UsageBucketSnapshot) map[string]UsageBucketSnapshot {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]UsageBucketSnapshot, len(source))
	for key, bucket := range source {
		result[key] = bucket
	}
	return result
}

func cloneCredentialBuckets(source map[string]map[usageCredentialKey]UsageBucketSnapshot) map[string]map[usageCredentialKey]UsageBucketSnapshot {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]map[usageCredentialKey]UsageBucketSnapshot, len(source))
	for hour, buckets := range source {
		cloned := make(map[usageCredentialKey]UsageBucketSnapshot, len(buckets))
		for key, bucket := range buckets {
			cloned[key] = bucket
		}
		result[hour] = cloned
	}
	return result
}

func boolCount(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func (s *RequestStatistics) pruneMinuteBucketsLocked(now time.Time) {
	cutoff := now.UTC().Add(-recentUsageMinuteRetention).Truncate(time.Minute)
	for key := range s.minuteBuckets {
		minute, err := time.Parse(time.RFC3339, key)
		if err != nil || minute.Before(cutoff) {
			delete(s.minuteBuckets, key)
		}
	}
}

func recentUsageBucket(buckets map[string]UsageBucketSnapshot, now time.Time) UsageBucketSnapshot {
	windowEnd := now.UTC()
	windowStart := windowEnd.Add(-recentUsageRateWindow)
	result := UsageBucketSnapshot{}
	for key, bucket := range buckets {
		minute, err := time.Parse(time.RFC3339, key)
		if err != nil || minute.Before(windowStart) || minute.After(windowEnd) {
			continue
		}
		result = mergeUsageBucket(result, bucket)
	}
	return result
}

func mergeUsageBucket(left, right UsageBucketSnapshot) UsageBucketSnapshot {
	left.TotalRequests += right.TotalRequests
	left.SuccessCount += right.SuccessCount
	left.FailureCount += right.FailureCount
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.CachedTokens += right.CachedTokens
	left.TotalTokens += right.TotalTokens
	left.TotalLatencyMS += right.TotalLatencyMS
	left.LatencySampleCount += right.LatencySampleCount
	return left
}

func (s *RequestStatistics) UsageOverview(filter servicedto.UsageFilter) UsageOverviewSnapshot {
	snapshot := s.Snapshot()
	overview := UsageOverviewSnapshot{
		Usage: StatisticsSnapshot{
			APIs:           make(map[string]*APISnapshot),
			RequestsByDay:  make(map[string]int64),
			RequestsByHour: make(map[string]int64),
			TokensByDay:    make(map[string]int64),
			TokensByHour:   make(map[string]int64),
		},
		Summary:      servicedto.UsageOverviewSummary{CostAvailable: false},
		Series:       newMemoryUsageOverviewSeries(),
		HourlySeries: newMemoryUsageOverviewSeries(),
		DailySeries:  newMemoryUsageOverviewSeries(),
		StartTime:    filter.StartTime,
		EndTime:      filter.EndTime,
	}
	overview.Summary.WindowMinutes = memoryUsageWindowMinutes(filter)
	overview.BucketByDay = memoryUsageBucketByDay(filter, overview.Summary.WindowMinutes)
	latestHourlyStart := memoryUsageLatestHourlyStart(filter.EndTime)

	for apiName, apiStats := range snapshot.APIs {
		if apiStats == nil {
			continue
		}
		apiResult := &APISnapshot{DisplayName: apiStats.DisplayName, Models: make(map[string]*ModelSnapshot)}
		for modelName, modelStats := range apiStats.Models {
			if modelStats == nil || (filter.Model != "" && modelName != filter.Model) {
				continue
			}
			modelResult := &ModelSnapshot{}
			if len(modelStats.Hourly) == 0 && filter.StartTime == nil && filter.EndTime == nil {
				modelResult.TotalRequests = modelStats.TotalRequests
				modelResult.SuccessCount = modelStats.SuccessCount
				modelResult.FailureCount = modelStats.FailureCount
				modelResult.InputTokens = modelStats.InputTokens
				modelResult.OutputTokens = modelStats.OutputTokens
				modelResult.ReasoningTokens = modelStats.ReasoningTokens
				modelResult.CachedTokens = modelStats.CachedTokens
				modelResult.TotalTokens = modelStats.TotalTokens
				modelResult.TotalLatencyMS = modelStats.TotalLatencyMS
				modelResult.LatencySampleCount = modelStats.LatencySampleCount
			}
			for hourKey, bucket := range modelStats.Hourly {
				hour, err := time.Parse(time.RFC3339, hourKey)
				if err != nil || !memoryUsageHourInRange(hour, filter) {
					continue
				}
				applyMemoryUsageBucketToModel(modelResult, bucket)
				dayKey := hour.In(time.Local).Format("2006-01-02")
				overview.Usage.RequestsByHour[hourKey] += bucket.TotalRequests
				overview.Usage.TokensByHour[hourKey] += bucket.TotalTokens
				overview.Usage.RequestsByDay[dayKey] += bucket.TotalRequests
				overview.Usage.TokensByDay[dayKey] += bucket.TotalTokens

				seriesKey, seriesMinutes := memoryUsageSeriesBucket(hour, overview.BucketByDay)
				applyMemoryUsageBucketToSeries(&overview.Series, modelName, seriesKey, seriesMinutes, bucket)
				if latestHourlyStart == nil || !hour.Before(*latestHourlyStart) {
					applyMemoryUsageBucketToSeries(&overview.HourlySeries, modelName, hourKey, 60, bucket)
				}
				applyMemoryUsageBucketToSeries(&overview.DailySeries, modelName, dayKey, 24*60, bucket)
			}
			if modelResult.TotalRequests == 0 {
				continue
			}
			apiResult.Models[modelName] = modelResult
			apiResult.TotalRequests += modelResult.TotalRequests
			apiResult.SuccessCount += modelResult.SuccessCount
			apiResult.FailureCount += modelResult.FailureCount
			apiResult.TotalTokens += modelResult.TotalTokens
			overview.Summary.CachedTokens += modelResult.CachedTokens
			overview.Summary.ReasoningTokens += modelResult.ReasoningTokens
		}
		if apiResult.TotalRequests == 0 {
			continue
		}
		overview.Usage.APIs[apiName] = apiResult
		overview.Usage.TotalRequests += apiResult.TotalRequests
		overview.Usage.SuccessCount += apiResult.SuccessCount
		overview.Usage.FailureCount += apiResult.FailureCount
		overview.Usage.TotalTokens += apiResult.TotalTokens
	}

	recentBucket := recentUsageBucket(snapshot.MinuteBuckets, time.Now().UTC())
	overview.Summary.WindowMinutes = int64(recentUsageRateWindow / time.Minute)
	overview.Summary.RequestCount = recentBucket.TotalRequests
	overview.Summary.TokenCount = recentBucket.TotalTokens
	overview.Summary.RPM = float64(recentBucket.TotalRequests) / float64(overview.Summary.WindowMinutes)
	overview.Summary.TPM = float64(recentBucket.TotalTokens) / float64(overview.Summary.WindowMinutes)
	fillMemoryUsageOverviewSeries(&overview, filter)
	return overview
}

func applyMemoryUsageBucketToModel(model *ModelSnapshot, bucket UsageBucketSnapshot) {
	model.TotalRequests += bucket.TotalRequests
	model.SuccessCount += bucket.SuccessCount
	model.FailureCount += bucket.FailureCount
	model.InputTokens += bucket.InputTokens
	model.OutputTokens += bucket.OutputTokens
	model.ReasoningTokens += bucket.ReasoningTokens
	model.CachedTokens += bucket.CachedTokens
	model.TotalTokens += bucket.TotalTokens
	model.TotalLatencyMS += bucket.TotalLatencyMS
	model.LatencySampleCount += bucket.LatencySampleCount
}

func newMemoryUsageOverviewSeries() servicedto.UsageOverviewSeries {
	return servicedto.UsageOverviewSeries{
		Requests:        make(map[string]int64),
		Tokens:          make(map[string]int64),
		RPM:             make(map[string]float64),
		TPM:             make(map[string]float64),
		Cost:            make(map[string]float64),
		InputTokens:     make(map[string]int64),
		OutputTokens:    make(map[string]int64),
		CachedTokens:    make(map[string]int64),
		ReasoningTokens: make(map[string]int64),
		Models:          make(map[string]servicedto.UsageOverviewSeries),
	}
}

func applyMemoryUsageBucketToSeries(series *servicedto.UsageOverviewSeries, modelName, key string, bucketMinutes int64, bucket UsageBucketSnapshot) {
	series.Requests[key] += bucket.TotalRequests
	series.Tokens[key] += bucket.TotalTokens
	series.InputTokens[key] += bucket.InputTokens
	series.OutputTokens[key] += bucket.OutputTokens
	series.CachedTokens[key] += bucket.CachedTokens
	series.ReasoningTokens[key] += bucket.ReasoningTokens
	series.RPM[key] = float64(series.Requests[key]) / float64(bucketMinutes)
	series.TPM[key] = float64(series.Tokens[key]) / float64(bucketMinutes)
	series.Cost[key] += 0

	modelSeries, ok := series.Models[modelName]
	if !ok {
		modelSeries = newMemoryUsageOverviewSeries()
	}
	modelSeries.Requests[key] += bucket.TotalRequests
	modelSeries.Tokens[key] += bucket.TotalTokens
	modelSeries.InputTokens[key] += bucket.InputTokens
	modelSeries.OutputTokens[key] += bucket.OutputTokens
	modelSeries.CachedTokens[key] += bucket.CachedTokens
	modelSeries.ReasoningTokens[key] += bucket.ReasoningTokens
	modelSeries.RPM[key] = float64(modelSeries.Requests[key]) / float64(bucketMinutes)
	modelSeries.TPM[key] = float64(modelSeries.Tokens[key]) / float64(bucketMinutes)
	modelSeries.Cost[key] += 0
	series.Models[modelName] = modelSeries
}

func memoryUsageHourInRange(hour time.Time, filter servicedto.UsageFilter) bool {
	if filter.StartTime != nil && hour.Before(filter.StartTime.UTC().Truncate(time.Hour)) {
		return false
	}
	if filter.EndTime != nil && hour.After(filter.EndTime.UTC()) {
		return false
	}
	return true
}

func memoryUsageWindowMinutes(filter servicedto.UsageFilter) int64 {
	if filter.StartTime == nil || filter.EndTime == nil {
		return 0
	}
	duration := filter.EndTime.UTC().Sub(filter.StartTime.UTC())
	if duration < 0 {
		return 0
	}
	minutes := int64((duration + time.Minute - 1) / time.Minute)
	return max(minutes, 1)
}

func memoryUsageBucketByDay(filter servicedto.UsageFilter, windowMinutes int64) bool {
	return filter.Range == "all" || filter.Range == "7d" || windowMinutes >= 7*24*60
}

func memoryUsageLatestHourlyStart(endTime *time.Time) *time.Time {
	if endTime == nil {
		return nil
	}
	start := endTime.UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	return &start
}

func memoryUsageSeriesBucket(timestamp time.Time, byDay bool) (string, int64) {
	if byDay {
		return timestamp.In(time.Local).Format("2006-01-02"), 24 * 60
	}
	return timestamp.UTC().Format("2006-01-02T15:00:00Z"), 60
}

func fillMemoryUsageOverviewSeries(overview *UsageOverviewSnapshot, filter servicedto.UsageFilter) {
	if filter.StartTime == nil || filter.EndTime == nil {
		return
	}
	fillMemoryUsageSeries(&overview.Series, *filter.StartTime, *filter.EndTime, overview.BucketByDay)
	hourlyStart := filter.StartTime.UTC()
	if latest := memoryUsageLatestHourlyStart(filter.EndTime); latest != nil && hourlyStart.Before(*latest) {
		hourlyStart = *latest
	}
	fillMemoryUsageSeries(&overview.HourlySeries, hourlyStart, *filter.EndTime, false)
	fillMemoryUsageSeries(&overview.DailySeries, *filter.StartTime, *filter.EndTime, true)
}

func fillMemoryUsageSeries(series *servicedto.UsageOverviewSeries, startTime, endTime time.Time, byDay bool) {
	var current time.Time
	var end time.Time
	var step time.Duration
	var format string
	var bucketMinutes int64
	if byDay {
		localStart := startTime.In(time.Local)
		localEnd := endTime.In(time.Local)
		current = time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, time.Local)
		end = time.Date(localEnd.Year(), localEnd.Month(), localEnd.Day(), 0, 0, 0, 0, time.Local)
		step = 24 * time.Hour
		format = "2006-01-02"
		bucketMinutes = 24 * 60
	} else {
		current = startTime.UTC().Truncate(time.Hour)
		end = endTime.UTC().Truncate(time.Hour)
		step = time.Hour
		format = "2006-01-02T15:00:00Z"
		bucketMinutes = 60
	}
	for !current.After(end) {
		key := current.Format(format)
		if _, ok := series.Requests[key]; !ok {
			series.Requests[key] = 0
			series.Tokens[key] = 0
			series.InputTokens[key] = 0
			series.OutputTokens[key] = 0
			series.CachedTokens[key] = 0
			series.ReasoningTokens[key] = 0
			series.RPM[key] = 0
			series.TPM[key] = 0
			series.Cost[key] = 0
		}
		for modelName, modelSeries := range series.Models {
			if _, ok := modelSeries.Requests[key]; !ok {
				modelSeries.Requests[key] = 0
				modelSeries.Tokens[key] = 0
				modelSeries.InputTokens[key] = 0
				modelSeries.OutputTokens[key] = 0
				modelSeries.CachedTokens[key] = 0
				modelSeries.ReasoningTokens[key] = 0
				modelSeries.RPM[key] = 0 / float64(bucketMinutes)
				modelSeries.TPM[key] = 0 / float64(bucketMinutes)
				modelSeries.Cost[key] = 0
				series.Models[modelName] = modelSeries
			}
		}
		current = current.Add(step)
	}
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

type recentUsageMinuteBucketProvider interface {
	GetRecentUsageMinuteBuckets(context.Context, servicedto.UsageFilter) (map[string]dto.UsageBucketSnapshot, error)
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
		TotalRequests:    aggregate.TotalRequests,
		SuccessCount:     aggregate.SuccessCount,
		FailureCount:     aggregate.FailureCount,
		TotalTokens:      aggregate.TotalTokens,
		APIs:             make(map[string]*APISnapshot, len(aggregate.APIs)),
		RequestsByDay:    aggregate.RequestsByDay,
		RequestsByHour:   aggregate.RequestsByHour,
		TokensByDay:      aggregate.TokensByDay,
		TokensByHour:     aggregate.TokensByHour,
		CredentialHourly: make(map[string]map[usageCredentialKey]UsageBucketSnapshot, len(aggregate.CredentialHourly)),
		MinuteBuckets:    make(map[string]UsageBucketSnapshot),
	}
	for hour, buckets := range aggregate.CredentialHourly {
		converted := make(map[usageCredentialKey]UsageBucketSnapshot, len(buckets))
		for _, bucket := range buckets {
			key := usageCredentialKey{source: bucket.Source, authIndex: bucket.AuthIndex}
			converted[key] = UsageBucketSnapshot{
				TotalRequests: bucket.SuccessCount + bucket.FailureCount,
				SuccessCount:  bucket.SuccessCount,
				FailureCount:  bucket.FailureCount,
				TotalTokens:   bucket.TotalTokens,
			}
		}
		snapshot.CredentialHourly[hour] = converted
	}
	for apiKey, apiStats := range aggregate.APIs {
		models := make(map[string]*ModelSnapshot, len(apiStats.Models))
		for modelName, modelStats := range apiStats.Models {
			hourly := make(map[string]UsageBucketSnapshot, len(modelStats.Hourly))
			for hour, bucket := range modelStats.Hourly {
				hourly[hour] = UsageBucketSnapshot{
					TotalRequests:      bucket.TotalRequests,
					SuccessCount:       bucket.SuccessCount,
					FailureCount:       bucket.FailureCount,
					InputTokens:        bucket.InputTokens,
					OutputTokens:       bucket.OutputTokens,
					ReasoningTokens:    bucket.ReasoningTokens,
					CachedTokens:       bucket.CachedTokens,
					TotalTokens:        bucket.TotalTokens,
					TotalLatencyMS:     bucket.TotalLatencyMS,
					LatencySampleCount: bucket.LatencySampleCount,
				}
			}
			models[modelName] = &ModelSnapshot{
				TotalRequests:      modelStats.TotalRequests,
				SuccessCount:       modelStats.SuccessCount,
				FailureCount:       modelStats.FailureCount,
				TotalTokens:        modelStats.TotalTokens,
				InputTokens:        modelStats.InputTokens,
				OutputTokens:       modelStats.OutputTokens,
				ReasoningTokens:    modelStats.ReasoningTokens,
				CachedTokens:       modelStats.CachedTokens,
				TotalLatencyMS:     modelStats.TotalLatencyMS,
				LatencySampleCount: modelStats.LatencySampleCount,
				Hourly:             hourly,
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
			modelSnapshot.ReasoningTokens = modelStats.ReasoningTokens
			modelSnapshot.CachedTokens = modelStats.CachedTokens
			modelSnapshot.TotalLatencyMS = modelStats.TotalLatencyMS
			modelSnapshot.LatencySampleCount = modelStats.LatencySampleCount
		}
	}
	now := time.Now().UTC()
	minuteCutoff := now.Add(-recentUsageMinuteRetention)
	if minuteProvider, ok := provider.(recentUsageMinuteBucketProvider); ok {
		minuteBuckets, errMinuteBuckets := minuteProvider.GetRecentUsageMinuteBuckets(ctx, servicedto.UsageFilter{
			Range:     "custom",
			StartTime: &minuteCutoff,
			EndTime:   &now,
		})
		if errMinuteBuckets != nil {
			return fmt.Errorf("load recent usage minute buckets: %w", errMinuteBuckets)
		}
		for minuteKey, bucket := range minuteBuckets {
			snapshot.MinuteBuckets[minuteKey] = UsageBucketSnapshot{
				TotalRequests:   bucket.TotalRequests,
				SuccessCount:    bucket.SuccessCount,
				FailureCount:    bucket.FailureCount,
				InputTokens:     bucket.InputTokens,
				OutputTokens:    bucket.OutputTokens,
				ReasoningTokens: bucket.ReasoningTokens,
				CachedTokens:    bucket.CachedTokens,
				TotalTokens:     bucket.TotalTokens,
			}
		}
	} else {
		for _, event := range eventsPage.Events {
			if event.Timestamp.Before(minuteCutoff) {
				continue
			}
			minuteKey := event.Timestamp.UTC().Truncate(time.Minute).Format(time.RFC3339)
			snapshot.MinuteBuckets[minuteKey] = mergeUsageBucket(snapshot.MinuteBuckets[minuteKey], UsageBucketSnapshot{
				TotalRequests:   1,
				SuccessCount:    boolCount(!event.Failed),
				FailureCount:    boolCount(event.Failed),
				InputTokens:     event.InputTokens,
				OutputTokens:    event.OutputTokens,
				ReasoningTokens: event.ReasoningTokens,
				CachedTokens:    event.CachedTokens,
				TotalTokens:     event.TotalTokens,
			})
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
	p.stats.record(
		record.APIKey,
		record.Model,
		record.Source,
		record.AuthIndex,
		record.RequestedAt,
		record.Failed,
		record.Detail.InputTokens,
		record.Detail.OutputTokens,
		record.Detail.ReasoningTokens,
		record.Detail.CachedTokens,
		totalTokens,
		record.Latency.Milliseconds(),
	)
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
