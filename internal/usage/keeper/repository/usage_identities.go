package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ReplaceUsageIdentitiesForAuthType(ctx context.Context, db *gorm.DB, identities []entities.UsageIdentity, authType entities.UsageIdentityAuthType, now time.Time) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}

	// Normalize and dedupe inputs first so upsert and stale checks share the same identity set.
	normalized, incomingIdentities := normalizeUsageIdentities(identities, authType)

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Upsert or restore identities from this sync so CPA-returned deleted rows become active again.
		if err := upsertUsageIdentities(tx, normalized); err != nil {
			return err
		}

		// Then stale-check only currently active identities for this auth_type; already-deleted history rows keep their deleted_at.
		return markStaleUsageIdentitiesDeleted(
			tx,
			tx.Model(&entities.UsageIdentity{}).Where("auth_type = ? AND is_deleted = ?", authType, false),
			incomingIdentities,
			now,
			"mark stale usage identities deleted",
		)
	})
}

func ReplaceUsageIdentitiesForProviderTypes(ctx context.Context, db *gorm.DB, identities []entities.UsageIdentity, providerTypes []string, now time.Time) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}

	// Provider metadata may only refresh AI provider identities; normalize types and identities first.
	normalized, incomingIdentities := normalizeUsageIdentities(identities, entities.UsageIdentityAuthTypeAIProvider)
	types := normalizeProviderTypes(providerTypes)

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Upsert successfully fetched provider identities first; historically deleted providers returned by CPA become active here.
		if err := upsertUsageIdentities(tx, normalized); err != nil {
			return err
		}
		if len(types) == 0 {
			return nil
		}

		// Batch fetched provider types to avoid oversized type IN clauses in extreme cases.
		for start := 0; start < len(types); start += insertBatchSize(entities.UsageIdentity{}) {
			end := min(start+insertBatchSize(entities.UsageIdentity{}), len(types))
			// Each batch covers only successfully fetched provider types; still-active identities not returned are marked deleted.
			query := tx.Model(&entities.UsageIdentity{}).
				Where("auth_type = ? AND is_deleted = ?", entities.UsageIdentityAuthTypeAIProvider, false).
				Where("type IN ?", types[start:end])
			if err := markStaleUsageIdentitiesDeleted(tx, query, incomingIdentities, now, "mark stale provider usage identities deleted"); err != nil {
				return err
			}
		}

		return nil
	})
}

type ListUsageIdentitiesPageRequest struct {
	AuthType *entities.UsageIdentityAuthType
	Page     int
	PageSize int
}

func ListUsageIdentities(ctx context.Context, db *gorm.DB) ([]entities.UsageIdentity, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	// The usage identities page shows full active/deleted history, so do not filter on is_deleted here.
	var identities []entities.UsageIdentity
	if err := db.WithContext(ctx).Order("auth_type asc, name asc, id asc").Find(&identities).Error; err != nil {
		return nil, fmt.Errorf("list usage identities: %w", err)
	}
	return identities, nil
}

func ListActiveUsageIdentities(ctx context.Context, db *gorm.DB) ([]entities.UsageIdentity, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	// Resolution and filtering only need active identities; filter deleted rows in SQL so they never reach the in-memory resolver.
	var identities []entities.UsageIdentity
	if err := activeUsageIdentitiesQuery(db.WithContext(ctx), nil).Find(&identities).Error; err != nil {
		return nil, fmt.Errorf("list active usage identities: %w", err)
	}
	return identities, nil
}

func ListActiveUsageIdentitiesPage(ctx context.Context, db *gorm.DB, request ListUsageIdentitiesPageRequest) ([]entities.UsageIdentity, int64, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("database is nil")
	}
	page := request.Page
	if page <= 0 {
		page = 1
	}
	pageSize := request.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}

	// Count with the same filters first, then apply offset/limit for the current page.
	query := activeUsageIdentitiesQuery(db.WithContext(ctx), request.AuthType)
	var total int64
	if err := query.Model(&entities.UsageIdentity{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count active usage identities page: %w", err)
	}
	var identities []entities.UsageIdentity
	if err := query.Offset((page - 1) * pageSize).Limit(pageSize).Find(&identities).Error; err != nil {
		return nil, 0, fmt.Errorf("list active usage identities page: %w", err)
	}
	return identities, total, nil
}

func activeUsageIdentitiesQuery(db *gorm.DB, authType *entities.UsageIdentityAuthType) *gorm.DB {
	// Centralize active and optional auth_type filters in one query builder so count/list stay aligned.
	query := db.Where("is_deleted = ?", false)
	if authType != nil {
		query = query.Where("auth_type = ?", *authType)
	}
	return query.Order("auth_type asc, name asc, id asc")
}

func GetActiveAuthFileUsageIdentityByAuthIndex(ctx context.Context, db *gorm.DB, authIndex string) (entities.UsageIdentity, error) {
	var identity entities.UsageIdentity
	if db == nil {
		return identity, fmt.Errorf("database is nil")
	}
	if err := db.WithContext(ctx).
		Where("auth_type = ? AND identity = ? AND is_deleted = ?", entities.UsageIdentityAuthTypeAuthFile, strings.TrimSpace(authIndex), false).
		First(&identity).Error; err != nil {
		return identity, fmt.Errorf("get active auth file usage identity by auth index: %w", err)
	}
	return identity, nil
}

func AggregateUsageIdentityStats(ctx context.Context, db *gorm.DB, now time.Time) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}

	// Aggregation must cover active and deleted identities so historically deleted ones keep accumulating matching usage_events.
	var identities []entities.UsageIdentity
	if err := db.WithContext(ctx).Find(&identities).Error; err != nil {
		return fmt.Errorf("list usage identities for aggregation: %w", err)
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, identity := range identities {
			delta, err := aggregateUsageIdentityDelta(tx, identity)
			if err != nil {
				return err
			}
			if delta.TotalRequests == 0 {
				continue
			}

			firstUsedAt := identity.FirstUsedAt
			if delta.FirstUsedAt != nil && (firstUsedAt == nil || delta.FirstUsedAt.Before(*firstUsedAt)) {
				first := *delta.FirstUsedAt
				firstUsedAt = &first
			}

			lastUsedAt := identity.LastUsedAt
			if delta.LastUsedAt != nil && (lastUsedAt == nil || delta.LastUsedAt.After(*lastUsedAt)) {
				last := *delta.LastUsedAt
				lastUsedAt = &last
			}

			updates := map[string]any{
				"total_requests":                 identity.TotalRequests + delta.TotalRequests,
				"success_count":                  identity.SuccessCount + delta.SuccessCount,
				"failure_count":                  identity.FailureCount + delta.FailureCount,
				"input_tokens":                   identity.InputTokens + delta.InputTokens,
				"output_tokens":                  identity.OutputTokens + delta.OutputTokens,
				"reasoning_tokens":               identity.ReasoningTokens + delta.ReasoningTokens,
				"cached_tokens":                  identity.CachedTokens + delta.CachedTokens,
				"total_tokens":                   identity.TotalTokens + delta.TotalTokens,
				"first_used_at":                  firstUsedAt,
				"last_used_at":                   lastUsedAt,
				"stats_updated_at":               now,
				"last_aggregated_usage_event_id": delta.MaxUsageEventID,
			}
			if err := tx.Model(&entities.UsageIdentity{}).Where("id = ?", identity.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update usage identity stats for %q: %w", identity.Identity, err)
			}
		}
		return nil
	})
}

func aggregateUsageIdentityDelta(tx *gorm.DB, identity entities.UsageIdentity) (dto.UsageIdentityStatsDelta, error) {
	var delta dto.UsageIdentityStatsDelta
	// Build usage_events filters by identity type first to avoid aggregating unrelated events.
	query, ok := usageIdentityEventsQuery(tx.Model(&entities.UsageEvent{}), identity)
	if !ok {
		return delta, nil
	}

	// Use last_aggregated_usage_event_id as an incremental cursor and only sum events after the last run.
	if err := query.
		Select(`
			COUNT(*) AS total_requests,
			COALESCE(SUM(CASE WHEN failed THEN 0 ELSE 1 END), 0) AS success_count,
			COALESCE(SUM(CASE WHEN failed THEN 1 ELSE 0 END), 0) AS failure_count,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(MAX(id), 0) AS max_usage_event_id`).
		Where("id > ?", identity.LastAggregatedUsageEventID).
		Scan(&delta).Error; err != nil {
		return delta, fmt.Errorf("aggregate usage identity stats for %q: %w", identity.Identity, err)
	}
	if delta.TotalRequests == 0 {
		return delta, nil
	}

	// Totals exclude first/last timestamps; those use the same identity filters to pick earliest and latest events.
	var firstEvent entities.UsageEvent
	firstQuery, _ := usageIdentityEventsQuery(tx.Model(&entities.UsageEvent{}), identity)
	if err := firstQuery.Where("id > ?", identity.LastAggregatedUsageEventID).Order("timestamp asc, id asc").First(&firstEvent).Error; err != nil {
		return delta, fmt.Errorf("find first usage identity event for %q: %w", identity.Identity, err)
	}
	firstUsedAt := firstEvent.Timestamp
	delta.FirstUsedAt = &firstUsedAt

	var lastEvent entities.UsageEvent
	lastQuery, _ := usageIdentityEventsQuery(tx.Model(&entities.UsageEvent{}), identity)
	if err := lastQuery.Where("id > ?", identity.LastAggregatedUsageEventID).Order("timestamp desc, id desc").First(&lastEvent).Error; err != nil {
		return delta, fmt.Errorf("find last usage identity event for %q: %w", identity.Identity, err)
	}
	lastUsedAt := lastEvent.Timestamp
	delta.LastUsedAt = &lastUsedAt

	return delta, nil
}

func usageIdentityEventsQuery(query *gorm.DB, identity entities.UsageIdentity) (*gorm.DB, bool) {
	var eventAuthType string
	switch identity.AuthType {
	case entities.UsageIdentityAuthTypeAuthFile:
		eventAuthType = "oauth"
	case entities.UsageIdentityAuthTypeAIProvider:
		eventAuthType = "apikey"
	default:
		return query, false
	}

	// usage_events and usage_identities are linked exactly by auth_index and identity.
	return query.Where("auth_type = ? AND auth_index = ?", eventAuthType, identity.Identity), true
}

func normalizeUsageIdentities(identities []entities.UsageIdentity, authType entities.UsageIdentityAuthType) ([]entities.UsageIdentity, []string) {
	normalized := make([]entities.UsageIdentity, 0, len(identities))
	incomingIdentities := make([]string, 0, len(identities))
	seen := make(map[string]struct{}, len(identities))

	for _, identity := range identities {
		authIndex := strings.TrimSpace(identity.Identity)
		if authIndex == "" {
			continue
		}
		if _, ok := seen[authIndex]; ok {
			continue
		}
		seen[authIndex] = struct{}{}
		incomingIdentities = append(incomingIdentities, authIndex)

		identity.ID = 0
		identity.AuthType = authType
		identity.Identity = authIndex
		identity.Name = strings.TrimSpace(identity.Name)
		identity.AuthTypeName = strings.TrimSpace(identity.AuthTypeName)
		identity.Type = strings.TrimSpace(identity.Type)
		identity.Provider = strings.TrimSpace(identity.Provider)
		identity.LookupKey = strings.TrimSpace(identity.LookupKey)
		identity.Prefix = strings.TrimSpace(identity.Prefix)
		identity.AccountID = trimOptionalString(identity.AccountID)
		identity.ProjectID = trimOptionalString(identity.ProjectID)
		identity.PlanType = trimOptionalString(identity.PlanType)
		identity.IsDeleted = false
		identity.DeletedAt = nil
		normalized = append(normalized, identity)
	}

	return normalized, incomingIdentities
}

func trimOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeProviderTypes(providerTypes []string) []string {
	seen := make(map[string]struct{}, len(providerTypes))
	types := make([]string, 0, len(providerTypes))
	for _, providerType := range providerTypes {
		providerType = strings.TrimSpace(providerType)
		if providerType == "" {
			continue
		}
		if _, ok := seen[providerType]; ok {
			continue
		}
		seen[providerType] = struct{}{}
		types = append(types, providerType)
	}
	return types
}

func markStaleUsageIdentitiesDeleted(tx *gorm.DB, query *gorm.DB, incomingIdentities []string, now time.Time, context string) error {
	// Keep this sync batch in an in-memory set to avoid huge identity NOT IN SQL.
	incoming := make(map[string]struct{}, len(incomingIdentities))
	for _, identity := range incomingIdentities {
		incoming[identity] = struct{}{}
	}

	// Load only minimal candidate columns from the DB; decide staleness in Go.
	var candidates []struct {
		ID       uint
		Identity string
	}
	if err := query.Select("id, identity").Find(&candidates).Error; err != nil {
		return fmt.Errorf("%s: %w", context, err)
	}

	// Candidate IDs absent from this input batch are stale and should be marked deleted.
	staleIDs := make([]uint, 0)
	for _, candidate := range candidates {
		if _, ok := incoming[candidate.Identity]; ok {
			continue
		}
		staleIDs = append(staleIDs, candidate.ID)
	}

	// Update stale IDs in batches to avoid SQLite variable limits on large id IN clauses.
	for start := 0; start < len(staleIDs); start += insertBatchSize(entities.UsageIdentity{}) {
		end := min(start+insertBatchSize(entities.UsageIdentity{}), len(staleIDs))
		if err := tx.Model(&entities.UsageIdentity{}).
			Where("id IN ?", staleIDs[start:end]).
			Updates(map[string]any{"is_deleted": true, "deleted_at": now}).Error; err != nil {
			return fmt.Errorf("%s: %w", context, err)
		}
	}
	return nil
}

func upsertUsageIdentities(tx *gorm.DB, identities []entities.UsageIdentity) error {
	if len(identities) == 0 {
		return nil
	}

	// On conflict, refresh only CPA-provided metadata and restore deleted rows; stats are owned by aggregation and are not overwritten here.
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "auth_type"}, {Name: "identity"}},
		DoUpdates: clause.Assignments(map[string]any{
			"name":           gorm.Expr("excluded.name"),
			"auth_type_name": gorm.Expr("excluded.auth_type_name"),
			"type":           gorm.Expr("excluded.type"),
			"provider":       gorm.Expr("excluded.provider"),
			"lookup_key":     gorm.Expr("excluded.lookup_key"),
			"prefix":         gorm.Expr("excluded.prefix"),
			"account_id":     gorm.Expr("excluded.account_id"),
			"project_id":     gorm.Expr("excluded.project_id"),
			"active_start":   gorm.Expr("excluded.active_start"),
			"active_until":   gorm.Expr("excluded.active_until"),
			"plan_type":      gorm.Expr("excluded.plan_type"),
			"is_deleted":     false,
			"deleted_at":     nil,
			"updated_at":     gorm.Expr("excluded.updated_at"),
		}),
	}).CreateInBatches(&identities, insertBatchSize(entities.UsageIdentity{})).Error; err != nil {
		return fmt.Errorf("upsert usage identities: %w", err)
	}
	return nil
}
