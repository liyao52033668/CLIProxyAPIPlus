package repository

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	usageEventRetentionDuration    = 30 * 24 * time.Hour
	usageEventKeyRetentionDuration = 90 * 24 * time.Hour
	usageArchiveBatchSize          = 1000
)

var (
	ErrDuplicateUsageAggregate     = errors.New("duplicate usage aggregate")
	ErrUsageAggregateOverlapsEvent = errors.New("usage aggregate overlaps live events")
)

func insertUsageEventsWithArchiveKeys(db *gorm.DB, events []entities.UsageEvent) (int, error) {
	hashes := make([]string, 0, len(events))
	hashByEventKey := make(map[string]string, len(events))
	eventKeysByHash := make(map[string][]string, len(events))
	for _, event := range events {
		hash := usageEventKeyHash(event.EventKey)
		if _, exists := hashByEventKey[event.EventKey]; exists {
			continue
		}
		hashByEventKey[event.EventKey] = hash
		eventKeysByHash[hash] = append(eventKeysByHash[hash], event.EventKey)
		hashes = append(hashes, hash)
	}

	inserted := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		archivedHashes, err := loadUsageEventKeyHashes(tx, hashes)
		if err != nil {
			return err
		}
		candidates := make([]entities.UsageEvent, 0, len(events))
		for _, event := range events {
			if _, archived := archivedHashes[hashByEventKey[event.EventKey]]; archived {
				continue
			}
			candidates = append(candidates, event)
		}
		for start := 0; start < len(candidates); start += insertBatchSize(entities.UsageEvent{}) {
			end := min(start+insertBatchSize(entities.UsageEvent{}), len(candidates))
			batch := candidates[start:end]
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "event_key"}},
				DoNothing: true,
			}).Create(&batch)
			if result.Error != nil {
				return fmt.Errorf("insert usage events: %w", result.Error)
			}
			inserted += int(result.RowsAffected)
		}

		currentArchivedHashes, err := loadUsageEventKeyHashes(tx, hashes)
		if err != nil {
			return err
		}
		var duplicateEventKeys []string
		for hash := range currentArchivedHashes {
			if _, previouslyArchived := archivedHashes[hash]; previouslyArchived {
				continue
			}
			duplicateEventKeys = append(duplicateEventKeys, eventKeysByHash[hash]...)
		}
		if len(duplicateEventKeys) > 0 {
			deleteResult := tx.Where("event_key IN ?", duplicateEventKeys).Delete(&entities.UsageEvent{})
			if deleteResult.Error != nil {
				return fmt.Errorf("delete concurrently archived usage events: %w", deleteResult.Error)
			}
			inserted -= int(deleteResult.RowsAffected)
		}
		return nil
	})
	return inserted, err
}

func loadUsageEventKeyHashes(db *gorm.DB, hashes []string) (map[string]struct{}, error) {
	archivedHashes := make(map[string]struct{})
	for start := 0; start < len(hashes); start += usageArchiveBatchSize {
		end := min(start+usageArchiveBatchSize, len(hashes))
		var found []string
		if err := db.Model(&entities.UsageEventKey{}).
			Where("event_key_hash IN ?", hashes[start:end]).
			Pluck("event_key_hash", &found).Error; err != nil {
			return nil, fmt.Errorf("load archived usage event keys: %w", err)
		}
		for _, hash := range found {
			archivedHashes[hash] = struct{}{}
		}
	}
	return archivedHashes, nil
}

func ensureUsageEventKeys(tx *gorm.DB, events []entities.UsageEvent) error {
	rowsByHash := make(map[string]entities.UsageEventKey, len(events))
	for _, event := range events {
		hash := usageEventKeyHash(event.EventKey)
		if _, exists := rowsByHash[hash]; exists {
			continue
		}
		rowsByHash[hash] = entities.UsageEventKey{
			EventKeyHash:   hash,
			EventTimestamp: event.Timestamp.UTC(),
		}
	}
	rows := make([]entities.UsageEventKey, 0, len(rowsByHash))
	for _, row := range rowsByHash {
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&rows, insertBatchSize(entities.UsageEventKey{})).Error; err != nil {
		return fmt.Errorf("store archived usage event keys: %w", err)
	}
	return nil
}

func usageEventKeyHash(eventKey string) string {
	sum := sha256.Sum256([]byte(eventKey))
	return hex.EncodeToString(sum[:])
}

func CleanupUsageEvents(db *gorm.DB, now time.Time) (dto.UsageEventCleanupResult, error) {
	if db == nil {
		return dto.UsageEventCleanupResult{}, fmt.Errorf("database is nil")
	}
	cutoff := now.UTC().Add(-usageEventRetentionDuration).Truncate(time.Hour)
	result := dto.UsageEventCleanupResult{Cutoff: cutoff}
	for {
		archived, aggregates, err := archiveUsageEventBatch(db, cutoff)
		result.ArchivedEvents += int64(archived)
		result.UpdatedAggregates += int64(aggregates)
		if err != nil {
			return result, err
		}
		if archived == 0 {
			break
		}
	}

	keyCutoff := now.UTC().Add(-usageEventKeyRetentionDuration)
	deleteResult := db.Where("event_timestamp < ?", keyCutoff).Delete(&entities.UsageEventKey{})
	if deleteResult.Error != nil {
		return result, fmt.Errorf("delete expired usage event keys: %w", deleteResult.Error)
	}
	result.DeletedEventKeys = deleteResult.RowsAffected
	return result, nil
}

func archiveUsageEventBatch(db *gorm.DB, cutoff time.Time) (int, int, error) {
	archived := 0
	aggregateCount := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("timestamp < ?", cutoff).
			Order("timestamp asc, id asc").
			Limit(usageArchiveBatchSize)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}

		var events []entities.UsageEvent
		if err := query.Find(&events).Error; err != nil {
			return fmt.Errorf("load expiring usage events: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		if err := ensureUsageEventKeys(tx, events); err != nil {
			return err
		}

		aggregates := buildUsageHourlyAggregates(events)
		if err := upsertUsageHourlyAggregates(tx, aggregates); err != nil {
			return err
		}

		ids := make([]uint, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		deleteResult := tx.Where("id IN ?", ids).Delete(&entities.UsageEvent{})
		if deleteResult.Error != nil {
			return fmt.Errorf("delete archived usage events: %w", deleteResult.Error)
		}
		if deleteResult.RowsAffected != int64(len(events)) {
			return fmt.Errorf("delete archived usage events: expected %d rows, deleted %d", len(events), deleteResult.RowsAffected)
		}
		archived = len(events)
		aggregateCount = len(aggregates)
		return nil
	})
	return archived, aggregateCount, err
}

func buildUsageHourlyAggregates(events []entities.UsageEvent) []entities.UsageHourlyAggregate {
	byKey := make(map[string]entities.UsageHourlyAggregate)
	for _, event := range events {
		bucketStart := event.Timestamp.UTC().Truncate(time.Hour)
		apiGroupKey := strings.TrimSpace(event.APIGroupKey)
		provider := strings.TrimSpace(event.Provider)
		authType := strings.TrimSpace(event.AuthType)
		model := strings.TrimSpace(event.Model)
		source := strings.TrimSpace(event.Source)
		authIndex := strings.TrimSpace(event.AuthIndex)
		key := usageHourlyAggregateKey(bucketStart, apiGroupKey, provider, authType, model, source, authIndex)
		aggregate := byKey[key]
		if aggregate.AggregateKey == "" {
			aggregate = entities.UsageHourlyAggregate{
				AggregateKey: key,
				BucketStart:  bucketStart,
				APIGroupKey:  apiGroupKey,
				Provider:     provider,
				AuthType:     authType,
				Model:        model,
				Source:       source,
				AuthIndex:    authIndex,
				FirstEventAt: event.Timestamp.UTC(),
				LastEventAt:  event.Timestamp.UTC(),
			}
		}
		aggregate.RequestCount++
		if event.Failed {
			aggregate.FailureCount++
		} else {
			aggregate.SuccessCount++
		}
		aggregate.InputTokens += event.InputTokens
		aggregate.OutputTokens += event.OutputTokens
		aggregate.ReasoningTokens += event.ReasoningTokens
		aggregate.CachedTokens += event.CachedTokens
		aggregate.TotalTokens += event.TotalTokens
		if event.LatencyMS > 0 {
			aggregate.TotalLatencyMS += event.LatencyMS
			aggregate.LatencySampleCount++
		}
		if event.Timestamp.UTC().Before(aggregate.FirstEventAt) {
			aggregate.FirstEventAt = event.Timestamp.UTC()
		}
		if event.Timestamp.UTC().After(aggregate.LastEventAt) {
			aggregate.LastEventAt = event.Timestamp.UTC()
		}
		byKey[key] = aggregate
	}

	aggregates := make([]entities.UsageHourlyAggregate, 0, len(byKey))
	for _, aggregate := range byKey {
		aggregates = append(aggregates, aggregate)
	}
	return aggregates
}

func usageHourlyAggregateKey(bucketStart time.Time, dimensions ...string) string {
	hash := sha256.New()
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(bucketStart.UTC().Unix()))
	_, _ = hash.Write(number[:])
	for _, dimension := range dimensions {
		binary.BigEndian.PutUint64(number[:], uint64(len(dimension)))
		_, _ = hash.Write(number[:])
		_, _ = hash.Write([]byte(dimension))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func upsertUsageHourlyAggregates(tx *gorm.DB, aggregates []entities.UsageHourlyAggregate) error {
	if len(aggregates) == 0 {
		return nil
	}
	updates := clause.Assignments(map[string]any{
		"request_count":        gorm.Expr("request_count + excluded.request_count"),
		"success_count":        gorm.Expr("success_count + excluded.success_count"),
		"failure_count":        gorm.Expr("failure_count + excluded.failure_count"),
		"input_tokens":         gorm.Expr("input_tokens + excluded.input_tokens"),
		"output_tokens":        gorm.Expr("output_tokens + excluded.output_tokens"),
		"reasoning_tokens":     gorm.Expr("reasoning_tokens + excluded.reasoning_tokens"),
		"cached_tokens":        gorm.Expr("cached_tokens + excluded.cached_tokens"),
		"total_tokens":         gorm.Expr("total_tokens + excluded.total_tokens"),
		"total_latency_ms":     gorm.Expr("total_latency_ms + excluded.total_latency_ms"),
		"latency_sample_count": gorm.Expr("latency_sample_count + excluded.latency_sample_count"),
		"first_event_at":       gorm.Expr("CASE WHEN excluded.first_event_at < first_event_at THEN excluded.first_event_at ELSE first_event_at END"),
		"last_event_at":        gorm.Expr("CASE WHEN excluded.last_event_at > last_event_at THEN excluded.last_event_at ELSE last_event_at END"),
		"updated_at":           gorm.Expr("excluded.updated_at"),
	})
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "aggregate_key"}},
		DoUpdates: updates,
	}).CreateInBatches(&aggregates, insertBatchSize(entities.UsageHourlyAggregate{})).Error; err != nil {
		return fmt.Errorf("upsert usage hourly aggregates: %w", err)
	}
	return nil
}

func applyUsageAggregateQueryWindow(query *gorm.DB, filter dto.UsageQueryFilter) *gorm.DB {
	if filter.StartTime != nil {
		query = query.Where("bucket_start >= ?", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		fullBucketEnd := filter.EndTime.UTC().Add(time.Nanosecond).Truncate(time.Hour)
		query = query.Where("bucket_start < ?", fullBucketEnd)
	}
	return query
}

func NormalizeUsageHourlyAggregate(aggregate entities.UsageHourlyAggregate) entities.UsageHourlyAggregate {
	aggregate.BucketStart = aggregate.BucketStart.UTC().Truncate(time.Hour)
	aggregate.APIGroupKey = strings.TrimSpace(aggregate.APIGroupKey)
	aggregate.Provider = strings.TrimSpace(aggregate.Provider)
	aggregate.AuthType = strings.TrimSpace(aggregate.AuthType)
	aggregate.Model = strings.TrimSpace(aggregate.Model)
	aggregate.Source = strings.TrimSpace(aggregate.Source)
	aggregate.AuthIndex = strings.TrimSpace(aggregate.AuthIndex)
	aggregate.AggregateKey = usageHourlyAggregateKey(aggregate.BucketStart, aggregate.APIGroupKey, aggregate.Provider, aggregate.AuthType, aggregate.Model, aggregate.Source, aggregate.AuthIndex)
	return aggregate
}

func InsertUsageHourlyAggregates(db *gorm.DB, aggregates []entities.UsageHourlyAggregate) (int64, int64, error) {
	if db == nil {
		return 0, 0, fmt.Errorf("database is nil")
	}
	if len(aggregates) == 0 {
		return 0, 0, nil
	}
	normalizedByKey := make(map[string]entities.UsageHourlyAggregate, len(aggregates))
	var totalRequests int64
	for _, aggregate := range aggregates {
		aggregate = NormalizeUsageHourlyAggregate(aggregate)
		if _, exists := normalizedByKey[aggregate.AggregateKey]; exists {
			return 0, 0, fmt.Errorf("%w: %s", ErrDuplicateUsageAggregate, aggregate.AggregateKey)
		}
		normalizedByKey[aggregate.AggregateKey] = aggregate
		totalRequests += aggregate.RequestCount
	}
	if err := rejectUsageAggregatesOverlappingEvents(db, normalizedByKey); err != nil {
		return 0, 0, err
	}

	keys := make([]string, 0, len(normalizedByKey))
	for key := range normalizedByKey {
		keys = append(keys, key)
	}
	existing := make(map[string]struct{})
	for start := 0; start < len(keys); start += usageArchiveBatchSize {
		end := min(start+usageArchiveBatchSize, len(keys))
		var found []string
		if err := db.Model(&entities.UsageHourlyAggregate{}).
			Where("aggregate_key IN ?", keys[start:end]).
			Pluck("aggregate_key", &found).Error; err != nil {
			return 0, 0, fmt.Errorf("load existing usage hourly aggregates: %w", err)
		}
		for _, key := range found {
			existing[key] = struct{}{}
		}
	}
	rows := make([]entities.UsageHourlyAggregate, 0, len(normalizedByKey)-len(existing))
	var addedRequests int64
	for key, aggregate := range normalizedByKey {
		if _, exists := existing[key]; exists {
			continue
		}
		rows = append(rows, aggregate)
		addedRequests += aggregate.RequestCount
	}
	if len(rows) > 0 {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(&rows, insertBatchSize(entities.UsageHourlyAggregate{})).Error; err != nil {
			return 0, 0, fmt.Errorf("insert usage hourly aggregates: %w", err)
		}
	}
	return addedRequests, totalRequests - addedRequests, nil
}

func rejectUsageAggregatesOverlappingEvents(db *gorm.DB, aggregates map[string]entities.UsageHourlyAggregate) error {
	keysByBucket := make(map[time.Time]map[string]struct{})
	for key, aggregate := range aggregates {
		if keysByBucket[aggregate.BucketStart] == nil {
			keysByBucket[aggregate.BucketStart] = make(map[string]struct{})
		}
		keysByBucket[aggregate.BucketStart][key] = struct{}{}
	}
	for bucketStart, keys := range keysByBucket {
		var events []entities.UsageEvent
		if err := db.Model(&entities.UsageEvent{}).
			Select("timestamp", "api_group_key", "provider", "auth_type", "model", "source", "auth_index").
			Where("timestamp >= ? AND timestamp < ?", bucketStart, bucketStart.Add(time.Hour)).
			Find(&events).Error; err != nil {
			return fmt.Errorf("load overlapping usage events: %w", err)
		}
		for _, event := range events {
			key := usageHourlyAggregateKey(
				event.Timestamp.UTC().Truncate(time.Hour),
				strings.TrimSpace(event.APIGroupKey),
				strings.TrimSpace(event.Provider),
				strings.TrimSpace(event.AuthType),
				strings.TrimSpace(event.Model),
				strings.TrimSpace(event.Source),
				strings.TrimSpace(event.AuthIndex),
			)
			if _, overlaps := keys[key]; overlaps {
				return fmt.Errorf("%w: %s", ErrUsageAggregateOverlapsEvent, key)
			}
		}
	}
	return nil
}

func GetUsageRequestTotals(db *gorm.DB) (int64, int64, error) {
	if db == nil {
		return 0, 0, fmt.Errorf("database is nil")
	}
	var eventTotals struct {
		Requests int64
		Failures int64
	}
	if err := db.Model(&entities.UsageEvent{}).
		Select("COUNT(*) AS requests, COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0) AS failures").
		Scan(&eventTotals).Error; err != nil {
		return 0, 0, fmt.Errorf("count usage events: %w", err)
	}
	var aggregateTotals struct {
		Requests int64
		Failures int64
	}
	if err := db.Model(&entities.UsageHourlyAggregate{}).
		Select("COALESCE(SUM(request_count), 0) AS requests, COALESCE(SUM(failure_count), 0) AS failures").
		Scan(&aggregateTotals).Error; err != nil {
		return 0, 0, fmt.Errorf("count usage aggregates: %w", err)
	}
	return eventTotals.Requests + aggregateTotals.Requests, eventTotals.Failures + aggregateTotals.Failures, nil
}

func StreamUsageHourlyAggregatesForExport(ctx context.Context, db *gorm.DB, filter dto.UsageQueryFilter, handle func(entities.UsageHourlyAggregate) error) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	return streamUsageHourlyAggregatesWithFilter(db.WithContext(ctx), filter, handle)
}

func streamUsageHourlyAggregatesWithFilter(db *gorm.DB, filter dto.UsageQueryFilter, handle func(entities.UsageHourlyAggregate) error) error {
	if handle == nil {
		return fmt.Errorf("usage aggregate handler is nil")
	}
	var lastBucket time.Time
	var lastKey string
	hasCursor := false
	for {
		query := applyUsageAggregateQueryWindow(db.Model(&entities.UsageHourlyAggregate{}), filter)
		if hasCursor {
			query = query.Where("(bucket_start > ?) OR (bucket_start = ? AND aggregate_key > ?)", lastBucket, lastBucket, lastKey)
		}
		var aggregates []entities.UsageHourlyAggregate
		if err := query.Order("bucket_start asc, aggregate_key asc").Limit(usageEventStreamBatchSize).Find(&aggregates).Error; err != nil {
			return fmt.Errorf("load usage hourly aggregates: %w", err)
		}
		if len(aggregates) == 0 {
			return nil
		}
		for _, aggregate := range aggregates {
			if err := handle(aggregate); err != nil {
				return err
			}
		}
		last := aggregates[len(aggregates)-1]
		lastBucket = last.BucketStart
		lastKey = last.AggregateKey
		hasCursor = true
		if len(aggregates) < usageEventStreamBatchSize {
			return nil
		}
	}
}
