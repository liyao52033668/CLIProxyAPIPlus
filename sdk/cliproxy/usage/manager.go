package usage

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository/dto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider  string
	Model     string
	Alias     string
	APIKey    string
	AuthID    string
	AuthIndex string
	AuthType  string
	Source    string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier for request event logs.
	ServiceTier string
	// RequestServiceTier explicitly aliases the client-requested service tier.
	RequestServiceTier string
	// ResponseServiceTier stores the final tier reported by the upstream response.
	ResponseServiceTier string
	RequestedAt         time.Time
	Latency             time.Duration
	TTFT                time.Duration
	Failed              bool
	Fail                Failure
	Detail              Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
}

// Failure holds HTTP failure metadata for an upstream request attempt.
type Failure struct {
	StatusCode int
	Body       string
}

// Detail holds the token usage breakdown.
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	// ResponseServiceTier stores the final tier reported by the upstream response body.
	ResponseServiceTier string
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}

// WithRequestedModelAlias stores the client-requested model name for usage sinks.
func WithRequestedModelAlias(ctx context.Context, alias string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedModelAliasContextKey{}, alias)
}

// RequestedModelAliasFromContext returns the client-requested model name stored in ctx.
func RequestedModelAliasFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(requestedModelAliasContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithReasoningEffort stores the resolved reasoning effort label for usage sinks.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

// ReasoningEffortFromContext returns the reasoning effort stored in ctx.
func ReasoningEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(reasoningEffortContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithServiceTier stores the resolved service tier label for usage sinks.
func WithServiceTier(ctx context.Context, tier string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tier = strings.TrimSpace(tier)
	if tier == "" {
		return ctx
	}
	return context.WithValue(ctx, serviceTierContextKey{}, tier)
}

// ServiceTierFromContext returns the service tier stored in ctx.
func ServiceTierFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(serviceTierContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager maintains a queue of usage records and delivers them to registered plugins.
type Manager struct {
	once     sync.Once
	stopOnce sync.Once
	cancel   context.CancelFunc

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []queueItem
	closed bool

	pluginsMu sync.RWMutex
	plugins   []Plugin

	db *gorm.DB
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	m := &Manager{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// NewManagerWithDB constructs a manager with database persistence.
func NewManagerWithDB(db *gorm.DB) *Manager {
	m := &Manager{db: db}
	m.cond = sync.NewCond(&m.mu)
	if db != nil {
		m.Register(newDatabasePlugin(db))
	}
	return m
}

// SetDB sets the database connection for the manager.
func (m *Manager) SetDB(db *gorm.DB) {
	if m == nil {
		return
	}
	m.pluginsMu.Lock()
	defer m.pluginsMu.Unlock()
	m.db = db
}

// GetDB returns the database connection for the manager.
func (m *Manager) GetDB() *gorm.DB {
	if m == nil {
		return nil
	}
	m.pluginsMu.RLock()
	defer m.pluginsMu.RUnlock()
	return m.db
}

// Start launches the background dispatcher. Calling Start multiple times is safe.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.once.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		var workerCtx context.Context
		workerCtx, m.cancel = context.WithCancel(ctx)
		go m.run(workerCtx)
	})
}

// Stop stops the dispatcher and flushes buffered database usage events.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cond.Broadcast()

		m.pluginsMu.RLock()
		plugins := make([]Plugin, len(m.plugins))
		copy(plugins, m.plugins)
		m.pluginsMu.RUnlock()
		for _, plugin := range plugins {
			if closer, ok := plugin.(*databasePlugin); ok {
				closer.Close()
			}
		}
	})
}

// Register appends a plugin to the delivery list.
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	m.Start(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) run(ctx context.Context) {
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

const (
	usageDBFlushMaxBatch = 100
	usageDBFlushInterval = 500 * time.Millisecond
)

// databasePlugin buffers usage records and flushes them in batches so each
// request does not open its own single-row INSERT against PostgreSQL.
type databasePlugin struct {
	db *gorm.DB

	mu     sync.Mutex
	buffer []entities.UsageEvent
	once   sync.Once
	stop   chan struct{}
	done   chan struct{}
}

func newDatabasePlugin(db *gorm.DB) *databasePlugin {
	return &databasePlugin{
		db:   db,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

func (p *databasePlugin) HandleUsage(ctx context.Context, record Record) {
	if p == nil || p.db == nil {
		return
	}
	p.once.Do(p.startFlusher)

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	event := entities.UsageEvent{
		EventKey:        buildEventKey(record, timestamp),
		APIGroupKey:     record.APIKey,
		Model:           record.Model,
		Provider:        record.Provider,
		AuthType:        record.AuthType,
		AuthIndex:       record.AuthIndex,
		Source:          record.Source,
		Timestamp:       timestamp,
		Failed:          record.Failed,
		LatencyMS:       record.Latency.Milliseconds(),
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
	}
	if event.TotalTokens == 0 {
		event.TotalTokens = event.InputTokens + event.OutputTokens + event.ReasoningTokens + event.CachedTokens
	}

	p.mu.Lock()
	p.buffer = append(p.buffer, event)
	shouldFlush := len(p.buffer) >= usageDBFlushMaxBatch
	p.mu.Unlock()
	if shouldFlush {
		p.flush()
	}
}

func (p *databasePlugin) startFlusher() {
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(usageDBFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.flush()
			case <-p.stop:
				p.flush()
				return
			}
		}
	}()
}

func (p *databasePlugin) flush() {
	if p == nil || p.db == nil {
		return
	}
	p.mu.Lock()
	if len(p.buffer) == 0 {
		p.mu.Unlock()
		return
	}
	batch := p.buffer
	p.buffer = nil
	p.mu.Unlock()

	if _, _, err := repository.InsertUsageEvents(p.db, batch); err != nil {
		log.Errorf("usage: batch insert failed (%d events): %v", len(batch), err)
	}
}

// Close flushes remaining buffered events. Safe to call multiple times.
func (p *databasePlugin) Close() {
	if p == nil {
		return
	}
	// Ensure the flusher exists so Close always has a terminal signal path.
	p.once.Do(p.startFlusher)
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
}

func buildEventKey(record Record, timestamp time.Time) string {
	tokens := dto.TokenStats{
		InputTokens:     record.Detail.InputTokens,
		OutputTokens:    record.Detail.OutputTokens,
		ReasoningTokens: record.Detail.ReasoningTokens,
		CachedTokens:    record.Detail.CachedTokens,
		TotalTokens:     record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return service.BuildEventKey(record.APIKey, record.Model, timestamp, record.Source, record.AuthIndex, record.Failed, tokens)
}

var defaultManager = NewManager(512)

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }

// SetDefaultManagerDB sets the database for the default manager.
func SetDefaultManagerDB(db *gorm.DB) {
	defaultManager.SetDB(db)
	defaultManager.Register(newDatabasePlugin(db))
}
