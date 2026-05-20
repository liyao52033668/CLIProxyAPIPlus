package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/redact"
	repodto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
)

type usageOverviewResponse struct {
	Usage         usageOverviewPayload       `json:"usage"`
	Summary       usageOverviewSummary       `json:"summary"`
	Series        usageOverviewSeries        `json:"series"`
	HourlySeries  usageOverviewSeries        `json:"hourly_series"`
	DailySeries   usageOverviewSeries        `json:"daily_series"`
	ServiceHealth usageOverviewServiceHealth `json:"service_health"`
	Timezone      string                     `json:"timezone"`
	RangeStart    *time.Time                 `json:"range_start,omitempty"`
	RangeEnd      *time.Time                 `json:"range_end,omitempty"`
}

type usageOverviewPayload struct {
	TotalRequests  int64                               `json:"total_requests"`
	SuccessCount   int64                               `json:"success_count"`
	FailureCount   int64                               `json:"failure_count"`
	TotalTokens    int64                               `json:"total_tokens"`
	APIs           map[string]usageOverviewAPISnapshot `json:"apis"`
	RequestsByDay  map[string]int64                    `json:"requests_by_day"`
	RequestsByHour map[string]int64                    `json:"requests_by_hour"`
	TokensByDay    map[string]int64                    `json:"tokens_by_day"`
	TokensByHour   map[string]int64                    `json:"tokens_by_hour"`
}

type usageOverviewSummary struct {
	RequestCount    int64   `json:"request_count"`
	TokenCount      int64   `json:"token_count"`
	WindowMinutes   int64   `json:"window_minutes"`
	RPM             float64 `json:"rpm"`
	TPM             float64 `json:"tpm"`
	TotalCost       float64 `json:"total_cost"`
	CostAvailable   bool    `json:"cost_available"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
}

type usageOverviewSeries struct {
	Requests        map[string]int64                   `json:"requests"`
	Tokens          map[string]int64                   `json:"tokens"`
	RPM             map[string]float64                 `json:"rpm"`
	TPM             map[string]float64                 `json:"tpm"`
	Cost            map[string]float64                 `json:"cost"`
	InputTokens     map[string]int64                   `json:"input_tokens"`
	OutputTokens    map[string]int64                   `json:"output_tokens"`
	CachedTokens    map[string]int64                   `json:"cached_tokens"`
	ReasoningTokens map[string]int64                   `json:"reasoning_tokens"`
	Models          map[string]usageOverviewSeriesLine `json:"models"`
}

type usageOverviewSeriesLine struct {
	Requests        map[string]int64   `json:"requests"`
	Tokens          map[string]int64   `json:"tokens"`
	RPM             map[string]float64 `json:"rpm"`
	TPM             map[string]float64 `json:"tpm"`
	Cost            map[string]float64 `json:"cost"`
	InputTokens     map[string]int64   `json:"input_tokens"`
	OutputTokens    map[string]int64   `json:"output_tokens"`
	CachedTokens    map[string]int64   `json:"cached_tokens"`
	ReasoningTokens map[string]int64   `json:"reasoning_tokens"`
}

type usageOverviewServiceHealth struct {
	TotalSuccess  int64                             `json:"total_success"`
	TotalFailure  int64                             `json:"total_failure"`
	SuccessRate   float64                           `json:"success_rate"`
	Rows          int                               `json:"rows"`
	Columns       int                               `json:"columns"`
	BucketSeconds int64                             `json:"bucket_seconds"`
	WindowStart   time.Time                         `json:"window_start"`
	WindowEnd     time.Time                         `json:"window_end"`
	BlockDetails  []usageOverviewServiceHealthBlock `json:"block_details"`
}

type usageOverviewServiceHealthBlock struct {
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Success   int64     `json:"success"`
	Failure   int64     `json:"failure"`
	Rate      float64   `json:"rate"`
}

type usageOverviewAPISnapshot struct {
	DisplayName   string                                `json:"display_name,omitempty"`
	TotalRequests int64                                 `json:"total_requests"`
	SuccessCount  int64                                 `json:"success_count"`
	FailureCount  int64                                 `json:"failure_count"`
	TotalTokens   int64                                 `json:"total_tokens"`
	Models        map[string]usageOverviewModelSnapshot `json:"models"`
}

type usageOverviewModelSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
}

func registerUsageOverviewRoute(router gin.IRoutes, usageProvider service.UsageProvider) {
	router.GET("/usage/overview", func(c *gin.Context) {
		if usageProvider == nil {
			c.JSON(http.StatusOK, usageOverviewResponse{
				Usage:         buildUsageOverviewPayload(nil),
				Summary:       usageOverviewSummary{},
				Series:        emptyUsageOverviewSeries(),
				HourlySeries:  emptyUsageOverviewSeries(),
				DailySeries:   emptyUsageOverviewSeries(),
				ServiceHealth: usageOverviewServiceHealth{BlockDetails: []usageOverviewServiceHealthBlock{}},
				Timezone:      time.Local.String(),
			})
			return
		}

		filter, err := parseUsageFilterQuery(c.Request, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		overview, err := usageProvider.GetUsageOverview(c.Request.Context(), filter)
		if err != nil {
			writeInternalError(c, "get usage overview failed", err)
			return
		}

		var usage *repodto.StatisticsSnapshot
		if overview != nil {
			usage = overview.Usage
		}
		redactedUsage := redact.UsageSnapshot(usage)
		c.JSON(http.StatusOK, usageOverviewResponse{
			Usage:         buildUsageOverviewPayload(redactedUsage),
			Summary:       buildUsageOverviewSummary(overview),
			Series:        buildUsageOverviewSeries(overview),
			HourlySeries:  buildUsageOverviewHourlySeries(overview),
			DailySeries:   buildUsageOverviewDailySeries(overview),
			ServiceHealth: buildUsageOverviewServiceHealth(overview),
			Timezone:      time.Local.String(),
			RangeStart:    filter.StartTime,
			RangeEnd:      filter.EndTime,
		})
	})
}

func buildUsageOverviewPayload(snapshot *repodto.StatisticsSnapshot) usageOverviewPayload {
	if snapshot == nil {
		return usageOverviewPayload{
			APIs:           map[string]usageOverviewAPISnapshot{},
			RequestsByDay:  map[string]int64{},
			RequestsByHour: map[string]int64{},
			TokensByDay:    map[string]int64{},
			TokensByHour:   map[string]int64{},
		}
	}

	payload := usageOverviewPayload{
		TotalRequests:  snapshot.TotalRequests,
		SuccessCount:   snapshot.SuccessCount,
		FailureCount:   snapshot.FailureCount,
		TotalTokens:    snapshot.TotalTokens,
		RequestsByDay:  cloneInt64Map(snapshot.RequestsByDay),
		RequestsByHour: cloneInt64Map(snapshot.RequestsByHour),
		TokensByDay:    cloneInt64Map(snapshot.TokensByDay),
		TokensByHour:   cloneInt64Map(snapshot.TokensByHour),
		APIs:           map[string]usageOverviewAPISnapshot{},
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		payloadAPI := usageOverviewAPISnapshot{
			DisplayName:   apiSnapshot.DisplayName,
			TotalRequests: apiSnapshot.TotalRequests,
			SuccessCount:  apiSnapshot.SuccessCount,
			FailureCount:  apiSnapshot.FailureCount,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        map[string]usageOverviewModelSnapshot{},
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			payloadAPI.Models[modelName] = usageOverviewModelSnapshot{
				TotalRequests: modelSnapshot.TotalRequests,
				SuccessCount:  modelSnapshot.SuccessCount,
				FailureCount:  modelSnapshot.FailureCount,
				TotalTokens:   modelSnapshot.TotalTokens,
			}
		}
		payload.APIs[apiName] = payloadAPI
	}

	return payload
}

func buildUsageOverviewSummary(overview *servicedto.UsageOverviewSnapshot) usageOverviewSummary {
	if overview == nil {
		return usageOverviewSummary{}
	}
	return usageOverviewSummary{
		RequestCount:    overview.Summary.RequestCount,
		TokenCount:      overview.Summary.TokenCount,
		WindowMinutes:   overview.Summary.WindowMinutes,
		RPM:             overview.Summary.RPM,
		TPM:             overview.Summary.TPM,
		TotalCost:       overview.Summary.TotalCost,
		CostAvailable:   overview.Summary.CostAvailable,
		CachedTokens:    overview.Summary.CachedTokens,
		ReasoningTokens: overview.Summary.ReasoningTokens,
	}
}

func emptyUsageOverviewSeries() usageOverviewSeries {
	return usageOverviewSeries{
		Requests:        map[string]int64{},
		Tokens:          map[string]int64{},
		RPM:             map[string]float64{},
		TPM:             map[string]float64{},
		Cost:            map[string]float64{},
		InputTokens:     map[string]int64{},
		OutputTokens:    map[string]int64{},
		CachedTokens:    map[string]int64{},
		ReasoningTokens: map[string]int64{},
		Models:          map[string]usageOverviewSeriesLine{},
	}
}

func mapUsageOverviewSeriesLine(series servicedto.UsageOverviewSeries) usageOverviewSeriesLine {
	return usageOverviewSeriesLine{
		Requests:        cloneInt64Map(series.Requests),
		Tokens:          cloneInt64Map(series.Tokens),
		RPM:             cloneFloat64Map(series.RPM),
		TPM:             cloneFloat64Map(series.TPM),
		Cost:            cloneFloat64Map(series.Cost),
		InputTokens:     cloneInt64Map(series.InputTokens),
		OutputTokens:    cloneInt64Map(series.OutputTokens),
		CachedTokens:    cloneInt64Map(series.CachedTokens),
		ReasoningTokens: cloneInt64Map(series.ReasoningTokens),
	}
}

func mapUsageOverviewSeries(series servicedto.UsageOverviewSeries) usageOverviewSeries {
	models := make(map[string]usageOverviewSeriesLine, len(series.Models))
	for model, modelSeries := range series.Models {
		models[model] = mapUsageOverviewSeriesLine(modelSeries)
	}
	return usageOverviewSeries{
		Requests:        cloneInt64Map(series.Requests),
		Tokens:          cloneInt64Map(series.Tokens),
		RPM:             cloneFloat64Map(series.RPM),
		TPM:             cloneFloat64Map(series.TPM),
		Cost:            cloneFloat64Map(series.Cost),
		InputTokens:     cloneInt64Map(series.InputTokens),
		OutputTokens:    cloneInt64Map(series.OutputTokens),
		CachedTokens:    cloneInt64Map(series.CachedTokens),
		ReasoningTokens: cloneInt64Map(series.ReasoningTokens),
		Models:          models,
	}
}

func buildUsageOverviewSeries(overview *servicedto.UsageOverviewSnapshot) usageOverviewSeries {
	if overview == nil {
		return emptyUsageOverviewSeries()
	}
	series := mapUsageOverviewSeries(overview.Series)
	return fillEmptyTimePointsForAPI(series, overview.StartTime, overview.EndTime, overview.BucketByDay)
}

func buildUsageOverviewHourlySeries(overview *servicedto.UsageOverviewSnapshot) usageOverviewSeries {
	if overview == nil {
		return emptyUsageOverviewSeries()
	}
	series := mapUsageOverviewSeries(overview.HourlySeries)
	return fillEmptyTimePointsForAPI(series, overview.StartTime, overview.EndTime, false)
}

func buildUsageOverviewDailySeries(overview *servicedto.UsageOverviewSnapshot) usageOverviewSeries {
	if overview == nil {
		return emptyUsageOverviewSeries()
	}
	series := mapUsageOverviewSeries(overview.DailySeries)
	return fillEmptyTimePointsForAPI(series, overview.StartTime, overview.EndTime, true)
}

func buildUsageOverviewServiceHealth(overview *servicedto.UsageOverviewSnapshot) usageOverviewServiceHealth {
	if overview == nil {
		return usageOverviewServiceHealth{BlockDetails: []usageOverviewServiceHealthBlock{}}
	}
	blocks := make([]usageOverviewServiceHealthBlock, 0, len(overview.Health.BlockDetails))
	for _, block := range overview.Health.BlockDetails {
		blocks = append(blocks, usageOverviewServiceHealthBlock{
			StartTime: block.StartTime,
			EndTime:   block.EndTime,
			Success:   block.Success,
			Failure:   block.Failure,
			Rate:      block.Rate,
		})
	}
	return usageOverviewServiceHealth{
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

func cloneInt64Map(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return map[string]int64{}
	}
	cloned := make(map[string]int64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func fillEmptyTimePointsForAPI(series usageOverviewSeries, startTime, endTime *time.Time, byDay bool) usageOverviewSeries {
	if startTime == nil || endTime == nil {
		startTime, endTime = inferTimeRangeFromSeries(series, byDay)
		if startTime == nil || endTime == nil {
			return series
		}
	}

	var currentTime time.Time
	var step time.Duration
	var format string

	if byDay {
		currentTime = startTime.In(time.Local).Truncate(24 * time.Hour)
		endTimeVal := endTime.In(time.Local).Truncate(24 * time.Hour)
		step = 24 * time.Hour
		format = "2006-01-02"

		for !currentTime.After(endTimeVal) {
			key := currentTime.Format(format)
			fillEmptyTimePointForSeries(&series, key)
			currentTime = currentTime.Add(step)
		}
	} else {
		currentTime = startTime.UTC().Truncate(time.Hour)
		endTimeVal := endTime.UTC().Truncate(time.Hour)
		step = time.Hour
		format = "2006-01-02T15:00:00Z"

		for !currentTime.After(endTimeVal) {
			key := currentTime.Format(format)
			fillEmptyTimePointForSeries(&series, key)
			currentTime = currentTime.Add(step)
		}
	}

	return series
}

func fillEmptyTimePointForSeries(series *usageOverviewSeries, key string) {
	if _, exists := series.Requests[key]; !exists {
		series.Requests[key] = 0
		series.Tokens[key] = 0
		series.RPM[key] = 0
		series.TPM[key] = 0
		series.Cost[key] = 0
		series.InputTokens[key] = 0
		series.OutputTokens[key] = 0
		series.CachedTokens[key] = 0
		series.ReasoningTokens[key] = 0
	}

	for modelName, modelSeries := range series.Models {
		if _, exists := modelSeries.Requests[key]; !exists {
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

func inferTimeRangeFromSeries(series usageOverviewSeries, byDay bool) (*time.Time, *time.Time) {
	if len(series.Requests) == 0 {
		return nil, nil
	}

	var earliest, latest time.Time
	first := true

	for key := range series.Requests {
		var t time.Time
		var err error
		if byDay {
			t, err = time.Parse("2006-01-02", key)
		} else {
			t, err = time.Parse("2006-01-02T15:00:00Z", key)
		}
		if err != nil {
			continue
		}

		if first {
			earliest = t
			latest = t
			first = false
		} else {
			if t.Before(earliest) {
				earliest = t
			}
			if t.After(latest) {
				latest = t
			}
		}
	}

	if first {
		return nil, nil
	}

	return &earliest, &latest
}

func cloneFloat64Map(source map[string]float64) map[string]float64 {
	if len(source) == 0 {
		return map[string]float64{}
	}
	cloned := make(map[string]float64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
