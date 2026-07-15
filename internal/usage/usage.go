package usage

import (
	"context"
	"fmt"
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

type RequestStatistics struct {
	mu             sync.RWMutex
	apis           map[string]*APISnapshot
	requestsByDay  map[string]int64
	requestsByHour map[string]int64
	tokensByDay    map[string]int64
	tokensByHour   map[string]int64
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

func (s *RequestStatistics) HasData() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.apis) > 0
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
	aggregate, err := provider.GetUsageAggregateWithFilter(ctx, servicedto.UsageFilter{})
	if err != nil {
		return fmt.Errorf("restore request statistics aggregate: %w", err)
	}
	analysis, err := provider.GetUsageAnalysis(ctx, servicedto.UsageFilter{})
	if err != nil {
		return fmt.Errorf("restore request statistics analysis: %w", err)
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
	stats.ReplaceSnapshot(snapshot)
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
