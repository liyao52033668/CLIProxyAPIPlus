package poller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	servicedto "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/service/dto"
	"github.com/sirupsen/logrus"
)

// Redis inbox processing runs every 5 seconds: pull only persists raw Redis messages, while process independently consumes the local inbox on that interval.
const redisInboxProcessInterval = 5 * time.Second

type RedisBatchSyncer interface {
	PullRedisUsageInbox(ctx context.Context) (*servicedto.RedisInboxPullResult, error)
	ProcessRedisUsageInbox(ctx context.Context) (*servicedto.RedisBatchSyncResult, error)
}

type RedisDrainConfig struct {
	IdleInterval time.Duration
	ErrorBackoff time.Duration
}

type RedisDrain struct {
	syncer RedisBatchSyncer
	config RedisDrainConfig
	now    func() time.Time
	sleep  func(context.Context, time.Duration) bool

	mu             sync.Mutex
	running        bool
	lastRunAt      time.Time
	lastError      string
	lastWarning    string
	lastStatus     string
	pullRunning    bool
	processRunning bool
}

func NewRedisDrain(syncer RedisBatchSyncer, cfg RedisDrainConfig) *RedisDrain {
	return &RedisDrain{
		syncer: syncer,
		config: cfg,
		now:    time.Now,
		sleep:  sleepContext,
	}
}

// Run starts continuous Redis sync with one Pull-only goroutine and one Process-only goroutine that never wait on each other.
func (d *RedisDrain) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	d.setRunning(true)
	defer d.setRunning(false)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		d.runPullLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		d.runProcessLoop(ctx)
	}()
	<-ctx.Done()
	wg.Wait()
	return nil
}

// runPullLoop only LPOP from the CPA Redis queue into redis_usage_inboxes; it does not decode or write usage_events.
func (d *RedisDrain) runPullLoop(ctx context.Context) {
	logrus.WithField("idle_interval", d.config.IdleInterval.String()).Info("redis inbox pull task started")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		result, err := d.runRedisPull(ctx)
		if err != nil {
			if shouldLogSyncError(err) {
				logrus.WithError(err).Error("redis drain pull failed")
			}
			if !d.sleep(ctx, d.config.ErrorBackoff) {
				return
			}
			continue
		}
		if result != nil && result.Empty {
			if !d.sleep(ctx, d.config.IdleInterval) {
				return
			}
		}
	}
}

// runProcessLoop processes persisted inbox rows every 5 seconds; failed rows stay retryable and bad messages are marked without blocking later rows.
func (d *RedisDrain) runProcessLoop(ctx context.Context) {
	logrus.WithField("interval", redisInboxProcessInterval.String()).Info("redis inbox process task started")
	for {
		if !d.sleep(ctx, redisInboxProcessInterval) {
			return
		}
		result, err := d.runRedisProcess(ctx)
		if err != nil && !errors.Is(err, ErrSyncCompletedWithWarnings) {
			if shouldLogSyncError(err) {
				d.logBatchFailure(result, err)
			}
			continue
		}
	}
}

func (d *RedisDrain) logBatchFailure(result *servicedto.RedisBatchSyncResult, err error) {
	fields := logrus.Fields{
		"status":          "",
		"empty":           false,
		"inserted_events": 0,
		"deduped_events":  0,
	}
	if result != nil {
		fields["status"] = result.Status
		fields["empty"] = result.Empty
		fields["inserted_events"] = result.InsertedEvents
		fields["deduped_events"] = result.DedupedEvents
	}
	logrus.WithError(err).WithFields(fields).Error("redis drain batch failed")
}

func (d *RedisDrain) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Status{
		Running:     d.running,
		LastRunAt:   d.lastRunAt,
		LastError:   d.lastError,
		LastWarning: d.lastWarning,
		LastStatus:  d.lastStatus,
		SyncRunning: d.pullRunning || d.processRunning,
	}
}

// SyncNow is the manual sync entrypoint: in Redis mode it Pulls once then Processes once so users immediately see new data.
func (d *RedisDrain) SyncNow(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	if _, err := d.runRedisPull(ctx); err != nil {
		return err
	}
	_, err := d.runRedisProcess(ctx)
	return err
}

// runRedisPull only prevents Pull re-entry and never blocks Process, so Redis long-poll/backoff cannot skip local inbox cycles.
func (d *RedisDrain) runRedisPull(ctx context.Context) (*servicedto.RedisInboxPullResult, error) {
	d.mu.Lock()
	if d.pullRunning {
		d.mu.Unlock()
		return nil, ErrSyncAlreadyRunning
	}
	d.pullRunning = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.pullRunning = false
		d.mu.Unlock()
	}()

	result, err := d.syncer.PullRedisUsageInbox(ctx)
	d.recordPullResult(result, err)
	return result, err
}

// runRedisProcess only prevents Process re-entry and never blocks Pull; Process input must come from persisted redis_usage_inboxes.
func (d *RedisDrain) runRedisProcess(ctx context.Context) (*servicedto.RedisBatchSyncResult, error) {
	d.mu.Lock()
	if d.processRunning {
		d.mu.Unlock()
		return nil, ErrSyncAlreadyRunning
	}
	d.processRunning = true
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.processRunning = false
		d.mu.Unlock()
	}()

	result, err := d.syncer.ProcessRedisUsageInbox(ctx)
	returnErr := err
	if err != nil && result != nil && result.Status != "" && result.Status != "failed" {
		returnErr = fmt.Errorf("%w: %v", ErrSyncCompletedWithWarnings, err)
	}
	d.recordResult(result, err)
	return result, returnErr
}

func (d *RedisDrain) recordPullResult(result *servicedto.RedisInboxPullResult, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastRunAt = d.now().UTC()
	status := ""
	if result != nil {
		status = result.Status
	}
	if status == "" && err == nil {
		status = "completed"
	}
	d.lastStatus = status
	d.lastError = ""
	d.lastWarning = ""
	if err != nil {
		d.lastError = err.Error()
	}
}

func (d *RedisDrain) recordResult(result *servicedto.RedisBatchSyncResult, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastRunAt = d.now().UTC()
	status := ""
	if result != nil {
		status = result.Status
	}
	if status == "" && err == nil {
		status = "completed"
	}
	d.lastStatus = status
	d.lastError = ""
	d.lastWarning = ""
	if err != nil {
		if result != nil && result.Status != "" && result.Status != "failed" {
			d.lastWarning = err.Error()
		} else {
			d.lastError = err.Error()
		}
	}
}

func (d *RedisDrain) setRunning(running bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = running
}

func (d *RedisDrain) validate() error {
	if d == nil {
		return fmt.Errorf("redis drain is nil")
	}
	if d.syncer == nil {
		return fmt.Errorf("redis drain syncer is nil")
	}
	if d.config.IdleInterval <= 0 {
		return fmt.Errorf("redis drain idle interval must be greater than zero")
	}
	if d.config.ErrorBackoff <= 0 {
		return fmt.Errorf("redis drain error backoff must be greater than zero")
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.sleep == nil {
		d.sleep = sleepContext
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
