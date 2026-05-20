package usage

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
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
	mu   sync.RWMutex
	apis map[string]*APISnapshot
}

var globalStats *RequestStatistics
var once sync.Once

func GetRequestStatistics() *RequestStatistics {
	once.Do(func() {
		globalStats = &RequestStatistics{
			apis: make(map[string]*APISnapshot),
		}
	})
	return globalStats
}

func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis: make(map[string]*APISnapshot),
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

func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	return result
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
