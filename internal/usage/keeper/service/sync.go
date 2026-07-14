package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/authfiles"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/providerconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/response"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type MetadataFetcher interface {
	FetchAuthFiles(ctx context.Context) (*response.AuthFilesResult, error)
	FetchGeminiAPIKeys(ctx context.Context) (*response.ProviderKeyConfigResult, error)
	FetchClaudeAPIKeys(ctx context.Context) (*response.ProviderKeyConfigResult, error)
	FetchCodexAPIKeys(ctx context.Context) (*response.ProviderKeyConfigResult, error)
	FetchVertexAPIKeys(ctx context.Context) (*response.ProviderKeyConfigResult, error)
	FetchOpenAICompatibility(ctx context.Context) (*response.OpenAICompatibilityResult, error)
}

type CPAClientFetcher interface {
	MetadataFetcher
}

const redisInboxProcessLimit = 1000

const (
	syncMetadataOptional = false
	syncMetadataRequired = true
)

type SyncService struct {
	db              *gorm.DB
	client          CPAClientFetcher
	redisQueue      RedisQueue
	redisQueueKey   string
	metadataFetcher MetadataFetcher
	baseURL         string
	now             func() time.Time
}

func NewSyncService(db *gorm.DB, cfg config.Config) *SyncService {
	return NewSyncServiceWithOptions(db, SyncServiceOptions{
		BaseURL: cfg.CPABaseURL,
		Client:  cpa.NewClient(cfg.CPABaseURL, cfg.CPAManagementKey, cfg.RequestTimeout, cfg.TLSSkipVerify),
		RedisQueue: cpa.NewRedisQueueClientWithOptions(cpa.RedisQueueOptions{
			BaseURL:       cfg.CPABaseURL,
			RedisAddr:     cfg.RedisQueueAddr,
			ManagementKey: cfg.CPAManagementKey,
			Timeout:       cfg.RequestTimeout,
			QueueKey:      cfg.RedisQueueKey,
			BatchSize:     cfg.RedisQueueBatchSize,
			TLS:           cfg.RedisQueueTLS,
			TLSSkipVerify: cfg.TLSSkipVerify,
		}),
		RedisQueueKey: cfg.RedisQueueKey,
	})
}

type SyncServiceOptions struct {
	BaseURL         string
	Client          CPAClientFetcher
	MetadataFetcher MetadataFetcher
	RedisQueue      RedisQueue
	RedisQueueKey   string
	Now             func() time.Time
}

func NewSyncServiceWithOptions(db *gorm.DB, opts SyncServiceOptions) *SyncService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	metadataFetcher := opts.MetadataFetcher
	if metadataFetcher == nil {
		metadataFetcher = opts.Client
	}
	return &SyncService{
		db:              db,
		client:          opts.Client,
		redisQueue:      opts.RedisQueue,
		redisQueueKey:   redisQueueKey(opts.RedisQueueKey),
		metadataFetcher: metadataFetcher,
		baseURL:         strings.TrimSpace(opts.BaseURL),
		now:             now,
	}
}

func NewSyncServiceWithClient(db *gorm.DB, baseURL string, client CPAClientFetcher) *SyncService {
	return NewSyncServiceWithOptions(db, SyncServiceOptions{
		BaseURL: baseURL,
		Client:  client,
	})
}

func (s *SyncService) SyncOnce(ctx context.Context) error {
	_, err := s.SyncNow(ctx)
	return err
}

func (s *SyncService) SyncNow(ctx context.Context) (*servicedto.SyncResult, error) {
	if _, err := s.PullRedisUsageInbox(ctx); err != nil {
		return nil, err
	}
	result, err := s.ProcessRedisUsageInbox(ctx)
	return syncResultFromRedisBatch(result), err
}

func syncResultFromRedisBatch(result *servicedto.RedisBatchSyncResult) *servicedto.SyncResult {
	if result == nil {
		return nil
	}
	return &servicedto.SyncResult{
		Status:         result.Status,
		InsertedEvents: result.InsertedEvents,
		DedupedEvents:  result.DedupedEvents,
	}
}

func (s *SyncService) SyncStatus(ctx context.Context) (string, error) {
	result, err := s.SyncNow(ctx)
	if result == nil {
		return "", err
	}
	return result.Status, err
}

func (s *SyncService) SyncMetadata(ctx context.Context) error {
	if err := s.validate(syncMetadataRequired); err != nil {
		return err
	}
	logrus.Debug("metadata sync started")
	fetchedAt := s.now().UTC()
	authFilesResult, authFilesErr := s.metadataFetcher.FetchAuthFiles(ctx)
	providerConfig, fetchedProviderTypes, providerMetadataErr := fetchProviderMetadata(ctx, s.metadataFetcher)
	authSyncErr := syncAuthFiles(ctx, s.db, authFilesResult, authFilesErr, fetchedAt)
	providerSyncErr, providerWarningErr := syncProviderMetadata(ctx, s.db, providerConfig, fetchedProviderTypes, providerMetadataErr, fetchedAt)
	upsertErr := joinErrors(authSyncErr, providerSyncErr)
	var aggregateErr error
	if upsertErr == nil {
		aggregateErr = repository.AggregateUsageIdentityStats(ctx, s.db, fetchedAt)
		if aggregateErr != nil {
			aggregateErr = fmt.Errorf("aggregate usage identity stats: %w", aggregateErr)
		}
	}
	err := joinErrors(upsertErr, aggregateErr, providerWarningErr)
	fields := logrus.Fields{
		"status": "completed",
	}
	if err != nil {
		fields["status"] = "completed_with_warnings"
		fields["error"] = err.Error()
	}
	logrus.WithFields(fields).Debug("metadata sync finished")
	return err
}

// PullRedisUsageInbox only pops Redis messages and stores them unchanged in redis_usage_inboxes.
// Decoding and usage_events persistence remain separate local processing responsibilities.
func (s *SyncService) PullRedisUsageInbox(ctx context.Context) (*servicedto.RedisInboxPullResult, error) {
	if err := s.validate(syncMetadataOptional); err != nil {
		return nil, err
	}
	if s.redisQueue == nil {
		return nil, fmt.Errorf("sync service redis queue is nil")
	}

	fetchedAt := s.now().UTC()
	messages, err := s.redisQueue.PopUsage(ctx)
	if err != nil {
		return &servicedto.RedisInboxPullResult{Status: "failed"}, fmt.Errorf("fetch redis usage: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"queue_key":     s.redisQueueKey,
		"message_count": len(messages),
	}).Debug("redis usage batch popped")
	if len(messages) == 0 {
		return &servicedto.RedisInboxPullResult{Empty: true, Status: "empty"}, nil
	}

	inboxRows, err := insertRedisInboxMessages(s.db, s.redisQueueKey, messages, fetchedAt)
	if err != nil {
		return &servicedto.RedisInboxPullResult{Status: "failed"}, fmt.Errorf("insert redis usage inbox: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"queue_key": s.redisQueueKey,
		"row_count": len(inboxRows),
	}).Debug("redis usage inbox rows inserted")
	return &servicedto.RedisInboxPullResult{Status: "completed", InsertedRows: len(inboxRows)}, nil
}

// ProcessRedisUsageInbox reads pending or process_failed inbox rows and writes usage_events.
// Successfully processed rows retain only usage_event_key as the final event association.
func (s *SyncService) ProcessRedisUsageInbox(ctx context.Context) (*servicedto.RedisBatchSyncResult, error) {
	if err := s.validate(syncMetadataOptional); err != nil {
		return nil, err
	}
	fetchedAt := s.now().UTC()
	processableRows, err := repository.ListProcessableRedisUsageInbox(s.db, redisInboxProcessLimit)
	if err != nil {
		return &servicedto.RedisBatchSyncResult{Status: "failed"}, fmt.Errorf("list processable redis usage inbox: %w", err)
	}
	if len(processableRows) == 0 {
		return &servicedto.RedisBatchSyncResult{Empty: true, Status: "empty"}, nil
	}
	logrus.WithField("row_count", len(processableRows)).Debug("redis usage inbox rows found for processing")
	return s.processRedisInboxRows(processableRows, fetchedAt)
}

// CleanupRedisUsageInbox only cleans the Redis inbox table for tests and standalone maintenance.
func (s *SyncService) CleanupRedisUsageInbox(ctx context.Context) error {
	if err := s.validate(syncMetadataOptional); err != nil {
		return err
	}
	_, err := repository.CleanupRedisUsageInbox(s.db, s.now())
	return err
}

// CleanupStorage runs scheduled usage retention and storage maintenance.
func (s *SyncService) CleanupStorage(ctx context.Context) error {
	if err := s.validate(syncMetadataOptional); err != nil {
		return err
	}
	_, err := repository.CleanupStorage(s.db, s.now())
	return err
}

// SyncRedisBatch remains a compatibility entry point that drains local inbox rows before one Redis pull.
// Background jobs must use PullRedisUsageInbox, ProcessRedisUsageInbox, and CleanupStorage separately.
func (s *SyncService) SyncRedisBatch(ctx context.Context) (*servicedto.RedisBatchSyncResult, error) {
	if result, err := s.ProcessRedisUsageInbox(ctx); err != nil || result == nil || !result.Empty {
		return result, err
	}
	if _, err := s.PullRedisUsageInbox(ctx); err != nil {
		return &servicedto.RedisBatchSyncResult{Status: "failed"}, err
	}
	return s.ProcessRedisUsageInbox(ctx)
}

// processRedisInboxRows decodes stored messages and writes events without blocking valid rows on decode failures.
// Persistence failures become process_failed rows that ProcessRedisUsageInbox retries in ID order.
func (s *SyncService) processRedisInboxRows(inboxRows []entities.RedisUsageInbox, fetchedAt time.Time) (*servicedto.RedisBatchSyncResult, error) {
	logrus.WithField("row_count", len(inboxRows)).Debug("redis usage inbox processing started")
	validRows := make([]entities.RedisUsageInbox, 0, len(inboxRows))
	events := make([]entities.UsageEvent, 0, len(inboxRows))
	decodeErrs := make([]error, 0)
	for _, row := range inboxRows {
		event, _, decodeErr := DecodeRedisUsageMessage(row.RawMessage, fetchedAt)
		if decodeErr != nil {
			if markErr := repository.MarkRedisUsageInboxDecodeFailed(s.db, row.ID, decodeErr); markErr != nil {
				return &servicedto.RedisBatchSyncResult{Status: "failed"}, fmt.Errorf("mark redis usage inbox decode failed: %w", markErr)
			}
			decodeErrs = append(decodeErrs, decodeErr)
			continue
		}
		validRows = append(validRows, row)
		events = append(events, event)
	}
	decodeErr := joinErrors(decodeErrs...)
	logrus.WithFields(logrus.Fields{
		"row_count":           len(inboxRows),
		"valid_event_count":   len(events),
		"decode_failed_count": len(decodeErrs),
	}).Debug("redis usage inbox rows decoded")
	if len(events) == 0 {
		if decodeErr != nil {
			return &servicedto.RedisBatchSyncResult{Status: "completed_with_warnings"}, decodeErr
		}
		return &servicedto.RedisBatchSyncResult{Empty: true, Status: "empty"}, nil
	}

	logrus.WithField("event_count", len(events)).Debug("redis usage events persistence started")
	result, err := s.persistRedisUsageEvents(events)
	if result == nil {
		markRedisInboxRowsProcessFailed(s.db, validRows, err)
		return nil, err
	}
	if err != nil && result.Status == "failed" {
		markRedisInboxRowsProcessFailed(s.db, validRows, err)
		return &servicedto.RedisBatchSyncResult{Status: result.Status}, err
	}
	for i, row := range validRows {
		if markErr := repository.MarkRedisUsageInboxProcessed(s.db, row.ID, events[i].EventKey, fetchedAt); markErr != nil {
			return &servicedto.RedisBatchSyncResult{Status: "failed"}, fmt.Errorf("mark redis usage inbox processed: %w", markErr)
		}
	}
	logrus.WithFields(logrus.Fields{
		"processed_rows":  len(validRows),
		"inserted_events": result.InsertedEvents,
		"deduped_events":  result.DedupedEvents,
		"status":          result.Status,
	}).Debug("redis usage inbox rows processed")

	status := result.Status
	returnErr := err
	if decodeErr != nil {
		status = "completed_with_warnings"
		if returnErr != nil {
			returnErr = joinErrors(returnErr, decodeErr)
		} else {
			returnErr = decodeErr
		}
	}
	return &servicedto.RedisBatchSyncResult{
		Status:         status,
		InsertedEvents: result.InsertedEvents,
		DedupedEvents:  result.DedupedEvents,
	}, returnErr
}

// persistRedisUsageEvents writes usage_events decoded from Redis inbox rows.
func (s *SyncService) persistRedisUsageEvents(events []entities.UsageEvent) (*servicedto.SyncResult, error) {
	var err error
	events, err = alignUsageEventKeysWithExistingCanonicalEvents(s.db, events)
	if err != nil {
		return &servicedto.SyncResult{Status: "failed"}, fmt.Errorf("align usage events: %w", err)
	}
	logrus.WithField("event_count", len(events)).Debug("usage events insert started")
	inserted, deduped, err := repository.InsertUsageEvents(s.db, events)
	if err != nil {
		return &servicedto.SyncResult{Status: "failed"}, fmt.Errorf("insert usage events: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"inserted_events": inserted,
		"deduped_events":  deduped,
	}).Debug("usage events insert finished")
	return &servicedto.SyncResult{Status: "completed", InsertedEvents: inserted, DedupedEvents: deduped}, nil
}

func alignUsageEventKeysWithExistingCanonicalEvents(db *gorm.DB, events []entities.UsageEvent) ([]entities.UsageEvent, error) {
	if len(events) == 0 {
		return events, nil
	}

	canonicalKeys := make(map[string]struct{}, len(events))
	lookupTimestamps := make(map[time.Time]struct{}, len(events))
	for i := range events {
		events[i].Timestamp = events[i].Timestamp.UTC()
		if strings.TrimSpace(events[i].RequestID) != "" {
			continue
		}
		canonicalKeys[canonicalUsageEventKey(events[i])] = struct{}{}
		lookupTimestamps[events[i].Timestamp] = struct{}{}
	}

	existingCanonicalKeys, err := loadExistingCanonicalUsageEventKeys(db, canonicalKeys, lookupTimestamps)
	if err != nil {
		return nil, err
	}
	processedCanonicalKeys, err := loadProcessedRedisInboxEventKeys(db, canonicalKeys)
	if err != nil {
		return nil, err
	}

	canonicalEventKeys := make(map[string]string, len(events))
	consumedCanonicalKeys := make(map[string]struct{}, len(events))
	for i := range events {
		canonicalKey := canonicalUsageEventKey(events[i])
		incomingKey := strings.TrimSpace(events[i].EventKey)
		if strings.TrimSpace(events[i].RequestID) != "" {
			canonicalEventKeys[canonicalKey] = incomingKey
			continue
		}
		if existingKey := canonicalEventKeys[canonicalKey]; existingKey != "" {
			if incomingKey == canonicalKey {
				events[i].EventKey = existingKey
			} else if existingKey == canonicalKey {
				if _, consumed := consumedCanonicalKeys[canonicalKey]; !consumed {
					events[i].EventKey = existingKey
					consumedCanonicalKeys[canonicalKey] = struct{}{}
				}
			}
			continue
		}

		existingKey, found := existingCanonicalKeys[canonicalKey]
		if !found {
			canonicalEventKeys[canonicalKey] = incomingKey
			continue
		}
		if existingKey != "" {
			if incomingKey == canonicalKey {
				events[i].EventKey = existingKey
			} else if existingKey == canonicalKey {
				if _, alreadyConsumed := processedCanonicalKeys[canonicalKey]; !alreadyConsumed {
					events[i].EventKey = existingKey
					consumedCanonicalKeys[canonicalKey] = struct{}{}
				}
			}
			canonicalEventKeys[canonicalKey] = existingKey
		} else {
			canonicalEventKeys[canonicalKey] = incomingKey
		}
	}
	return events, nil
}

const canonicalUsageEventLookupBatchSize = 500

func loadExistingCanonicalUsageEventKeys(db *gorm.DB, canonicalKeys map[string]struct{}, timestamps map[time.Time]struct{}) (map[string]string, error) {
	result := make(map[string]string, len(canonicalKeys))
	if len(canonicalKeys) == 0 {
		return result, nil
	}

	values := make([]time.Time, 0, len(timestamps))
	for timestamp := range timestamps {
		values = append(values, timestamp)
	}
	for start := 0; start < len(values); start += canonicalUsageEventLookupBatchSize {
		end := min(start+canonicalUsageEventLookupBatchSize, len(values))
		var existing []entities.UsageEvent
		err := db.Select([]string{
			"id",
			"event_key",
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
		}).Where("timestamp IN ?", values[start:end]).Order("id ASC").Find(&existing).Error
		if err != nil {
			return nil, fmt.Errorf("find equivalent usage events: %w", err)
		}
		for _, event := range existing {
			canonicalKey := canonicalUsageEventKey(event)
			if _, requested := canonicalKeys[canonicalKey]; !requested {
				continue
			}
			if _, loaded := result[canonicalKey]; !loaded {
				result[canonicalKey] = strings.TrimSpace(event.EventKey)
			}
		}
	}
	return result, nil
}

func loadProcessedRedisInboxEventKeys(db *gorm.DB, canonicalKeys map[string]struct{}) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(canonicalKeys))
	if len(canonicalKeys) == 0 {
		return result, nil
	}

	values := make([]string, 0, len(canonicalKeys))
	for canonicalKey := range canonicalKeys {
		values = append(values, canonicalKey)
	}
	for start := 0; start < len(values); start += canonicalUsageEventLookupBatchSize {
		end := min(start+canonicalUsageEventLookupBatchSize, len(values))
		var eventKeys []string
		err := db.Model(&entities.RedisUsageInbox{}).
			Distinct("usage_event_key").
			Where("status = ? AND usage_event_key IN ?", repository.RedisUsageInboxStatusProcessed, values[start:end]).
			Pluck("usage_event_key", &eventKeys).Error
		if err != nil {
			return nil, fmt.Errorf("list redis inbox references: %w", err)
		}
		for _, eventKey := range eventKeys {
			result[strings.TrimSpace(eventKey)] = struct{}{}
		}
	}
	return result, nil
}

func canonicalUsageEventKey(event entities.UsageEvent) string {
	return BuildEventKey(
		event.APIGroupKey,
		event.Model,
		event.Timestamp,
		event.Source,
		event.AuthIndex,
		event.Failed,
		dto.TokenStats{
			InputTokens:     event.InputTokens,
			OutputTokens:    event.OutputTokens,
			ReasoningTokens: event.ReasoningTokens,
			CachedTokens:    event.CachedTokens,
			TotalTokens:     event.TotalTokens,
		},
	)
}

func (s *SyncService) validate(syncMetadata bool) error {
	if s == nil {
		return fmt.Errorf("sync service is nil")
	}
	if s.db == nil {
		return fmt.Errorf("sync service database is nil")
	}
	if syncMetadata {
		if s.metadataFetcher == nil && s.client != nil {
			s.metadataFetcher = s.client
		}
		if s.metadataFetcher == nil {
			return fmt.Errorf("sync service metadata fetcher is nil")
		}
	}
	return nil
}

// insertRedisInboxMessages persists raw messages before decoding to prevent loss after LPOP.
func insertRedisInboxMessages(db *gorm.DB, queueKey string, messages []string, poppedAt time.Time) ([]entities.RedisUsageInbox, error) {
	inputs := make([]dto.RedisInboxInsert, 0, len(messages))
	for _, message := range messages {
		inputs = append(inputs, dto.RedisInboxInsert{
			QueueKey:   queueKey,
			RawMessage: message,
			PoppedAt:   poppedAt,
		})
	}
	return repository.InsertRedisUsageInboxMessages(db, inputs)
}

func markRedisInboxRowsProcessFailed(db *gorm.DB, rows []entities.RedisUsageInbox, err error) {
	if err == nil {
		return
	}
	for _, row := range rows {
		if markErr := repository.MarkRedisUsageInboxProcessFailed(db, row.ID, err); markErr != nil {
			logrus.WithError(markErr).WithField("inbox_id", row.ID).Warn("failed to mark redis usage inbox process failure")
			continue
		}
		var stored entities.RedisUsageInbox
		if loadErr := db.First(&stored, row.ID).Error; loadErr != nil {
			logrus.WithError(loadErr).WithField("inbox_id", row.ID).Warn("failed to load redis usage inbox after process failure")
			continue
		}
		if stored.Status == repository.RedisUsageInboxStatusDiscarded {
			logrus.WithFields(logrus.Fields{
				"inbox_id":      stored.ID,
				"queue_key":     stored.QueueKey,
				"message_hash":  stored.MessageHash,
				"attempt_count": stored.AttemptCount,
				"last_error":    stored.LastError,
				"popped_at":     stored.PoppedAt,
			}).Warn("discarded redis usage inbox row after repeated process failures")
		}
	}
}

func redisQueueKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return cpa.ManagementUsageQueueKey
	}
	return trimmed
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func syncAuthFiles(ctx context.Context, db *gorm.DB, result *response.AuthFilesResult, fetchErr error, now time.Time) error {
	if fetchErr != nil {
		return fmt.Errorf("fetch auth files: %w", fetchErr)
	}
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if result == nil {
		return fmt.Errorf("fetch auth files: empty response")
	}

	identities := make([]entities.UsageIdentity, 0, len(result.Payload.Files))
	for _, file := range result.Payload.Files {
		identities = append(identities, authFileUsageIdentity(file))
	}
	if err := repository.ReplaceUsageIdentitiesForAuthType(ctx, db, identities, entities.UsageIdentityAuthTypeAuthFile, now); err != nil {
		return fmt.Errorf("sync auth file usage identities: %w", err)
	}
	return nil
}

type authFileUsageIdentityExtension func(authfiles.AuthFile, *entities.UsageIdentity)

var authFileUsageIdentityExtensions = map[string]authFileUsageIdentityExtension{
	"codex": extendCodexAuthFileUsageIdentity,
}

// authFileUsageIdentity applies common identity fields before type-specific extensions.
func authFileUsageIdentity(file authfiles.AuthFile) entities.UsageIdentity {
	identity := baseAuthFileUsageIdentity(file)
	if extend, ok := authFileUsageIdentityExtensions[strings.ToLower(strings.TrimSpace(file.Type))]; ok {
		extend(file, &identity)
	}
	identity.ProjectID = resolveAuthFileProjectID(file)
	return identity
}

func baseAuthFileUsageIdentity(file authfiles.AuthFile) entities.UsageIdentity {
	return entities.UsageIdentity{
		Name:         firstNonEmpty(file.Email, file.Label, file.Name, file.AuthIndex),
		AuthType:     entities.UsageIdentityAuthTypeAuthFile,
		AuthTypeName: "oauth",
		Identity:     file.AuthIndex,
		Type:         file.Type,
		Provider:     file.Provider,
	}
}

// extendCodexAuthFileUsageIdentity stores ChatGPT id_token fields only when present.
func extendCodexAuthFileUsageIdentity(file authfiles.AuthFile, identity *entities.UsageIdentity) {
	identity.AccountID = resolveCodexAccountID(file)
	identity.ActiveStart = resolveCodexActiveStart(file)
	identity.ActiveUntil = resolveCodexActiveUntil(file)
	identity.PlanType = resolveCodexPlanType(file)
}

func fetchProviderMetadata(ctx context.Context, fetcher MetadataFetcher) (providerconfig.ProviderMetadataConfig, []string, error) {
	var cfg providerconfig.ProviderMetadataConfig
	var fetchedProviderTypes []string
	var errs []error

	if result, err := fetcher.FetchGeminiAPIKeys(ctx); err != nil {
		errs = append(errs, fmt.Errorf("fetch gemini api keys: %w", err))
	} else if result == nil {
		errs = append(errs, fmt.Errorf("gemini api keys response is nil"))
	} else {
		fetchedProviderTypes = append(fetchedProviderTypes, "gemini")
		cfg.GeminiAPIKeys = result.Payload
	}
	if result, err := fetcher.FetchClaudeAPIKeys(ctx); err != nil {
		errs = append(errs, fmt.Errorf("fetch claude api keys: %w", err))
	} else if result == nil {
		errs = append(errs, fmt.Errorf("claude api keys response is nil"))
	} else {
		fetchedProviderTypes = append(fetchedProviderTypes, "claude")
		cfg.ClaudeAPIKeys = result.Payload
	}
	if result, err := fetcher.FetchCodexAPIKeys(ctx); err != nil {
		errs = append(errs, fmt.Errorf("fetch codex api keys: %w", err))
	} else if result == nil {
		errs = append(errs, fmt.Errorf("codex api keys response is nil"))
	} else {
		fetchedProviderTypes = append(fetchedProviderTypes, "codex")
		cfg.CodexAPIKeys = result.Payload
	}
	if result, err := fetcher.FetchVertexAPIKeys(ctx); err != nil {
		errs = append(errs, fmt.Errorf("fetch vertex api keys: %w", err))
	} else if result == nil {
		errs = append(errs, fmt.Errorf("vertex api keys response is nil"))
	} else {
		fetchedProviderTypes = append(fetchedProviderTypes, "vertex")
		cfg.VertexAPIKeys = result.Payload
	}
	if result, err := fetcher.FetchOpenAICompatibility(ctx); err != nil {
		errs = append(errs, fmt.Errorf("fetch openai compatibility: %w", err))
	} else if result == nil {
		errs = append(errs, fmt.Errorf("openai compatibility response is nil"))
	} else {
		fetchedProviderTypes = append(fetchedProviderTypes, "openai")
		cfg.OpenAICompatibility = result.Payload
	}

	return cfg, fetchedProviderTypes, joinErrors(errs...)
}

func syncProviderMetadata(ctx context.Context, db *gorm.DB, cfg providerconfig.ProviderMetadataConfig, fetchedProviderTypes []string, fetchErr error, now time.Time) (error, error) {
	if db == nil {
		return fmt.Errorf("database is nil"), nil
	}

	inputs := flattenProviderMetadata(cfg)
	identities := providerMetadataUsageIdentities(inputs)
	if err := repository.ReplaceUsageIdentitiesForProviderTypes(ctx, db, identities, fetchedProviderTypes, now); err != nil {
		return fmt.Errorf("sync provider usage identities: %w", err), nil
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("fetch provider metadata: %w", fetchErr)
	}
	return nil, nil
}

func providerMetadataUsageIdentities(inputs []servicedto.ProviderMetadataInput) []entities.UsageIdentity {
	identities := make([]entities.UsageIdentity, 0, len(inputs))
	for _, input := range inputs {
		identities = append(identities, entities.UsageIdentity{
			Name:         input.DisplayName,
			AuthType:     entities.UsageIdentityAuthTypeAIProvider,
			AuthTypeName: "apikey",
			Identity:     input.AuthIndex,
			Type:         input.ProviderType,
			Provider:     input.DisplayName,
			LookupKey:    input.LookupKey,
			Prefix:       input.Prefix,
		})
	}
	return identities
}

func flattenProviderMetadata(cfg providerconfig.ProviderMetadataConfig) []servicedto.ProviderMetadataInput {
	items := make([]servicedto.ProviderMetadataInput, 0)
	seen := make(map[string]struct{})
	// Provider metadata creates auth-index identities and stores prefix as an attribute.
	appendItem := func(lookupKey, prefix, providerType, displayName, authIndex string) {
		lookupKey = strings.TrimSpace(lookupKey)
		prefix = strings.TrimSpace(prefix)
		providerType = strings.TrimSpace(providerType)
		displayName = strings.TrimSpace(displayName)
		authIndex = strings.TrimSpace(authIndex)
		if lookupKey == "" || providerType == "" || displayName == "" || authIndex == "" {
			return
		}
		if _, ok := seen[authIndex]; ok {
			return
		}
		seen[authIndex] = struct{}{}
		items = append(items, servicedto.ProviderMetadataInput{
			LookupKey:    lookupKey,
			Prefix:       prefix,
			ProviderType: providerType,
			DisplayName:  displayName,
			AuthIndex:    authIndex,
		})
	}
	appendProviderEntries := func(providerType string, configs []providerconfig.ProviderKeyConfig) {
		for _, cfg := range configs {
			displayName := firstNonEmpty(cfg.Name, providerType)
			appendItem(cfg.APIKey, cfg.Prefix, providerType, displayName, cfg.AuthIndex)
		}
	}

	appendProviderEntries("gemini", cfg.GeminiAPIKeys)
	appendProviderEntries("claude", cfg.ClaudeAPIKeys)
	appendProviderEntries("codex", cfg.CodexAPIKeys)
	appendProviderEntries("vertex", cfg.VertexAPIKeys)

	for _, provider := range cfg.OpenAICompatibility {
		displayName := firstNonEmpty(provider.Name, "openai")
		for _, entry := range provider.APIKeyEntries {
			appendItem(entry.APIKey, provider.Prefix, "openai", displayName, entry.AuthIndex)
		}
	}

	return items
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func joinErrors(errs ...error) error {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		messages = append(messages, strings.TrimSpace(err.Error()))
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "; "))
}
