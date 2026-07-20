package codexinspection

import (
	"context"
	"maps"
	"sort"
	"sync"
	"time"
)

type Runner interface {
	Run(ctx context.Context, req RunRequest) (LatestSnapshot, error)
}

type CompletionRunner interface {
	StartRunWithCompletion(ctx context.Context, req RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error)
}

type Worker struct {
	runner                    Runner
	mutex                     sync.Mutex
	settings                  InspectionSettings
	nextTriggerAtMSByProvider map[string]int64
	runningProviders          map[string]struct{}
	enabled                   bool
	reloadCh                  chan struct{}
}

func NewWorker(runner Runner) *Worker {
	return &Worker{
		runner:           runner,
		runningProviders: make(map[string]struct{}),
		reloadCh:         make(chan struct{}, 1),
	}
}

func runInspection(runner Runner, ctx context.Context, req RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error) {
	if completionRunner, ok := runner.(CompletionRunner); ok {
		snapshot, err := completionRunner.StartRunWithCompletion(ctx, req, onFinish)
		if err != nil && onFinish != nil {
			onFinish(snapshot)
		}
		return snapshot, err
	}
	snapshot, err := runner.Run(ctx, req)
	if onFinish != nil {
		onFinish(snapshot)
	}
	return snapshot, err
}

func (w *Worker) Enabled() bool {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.enabled
}

func (w *Worker) Reload(settings InspectionSettings, nextTriggerAtMSByProvider map[string]int64) {
	w.load(settings, nextTriggerAtMSByProvider)
	w.notifyReload()
}

func (w *Worker) TriggerNowForTest(ctx context.Context) {
	if w == nil || w.runner == nil {
		return
	}
	for _, provider := range w.enabledProviders() {
		_, _ = runInspection(w.runner, ctx, RunRequest{TriggerType: TriggerTypeScheduled, Provider: provider}, nil)
	}
}

func (w *Worker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	go w.loop(ctx)
}

func (w *Worker) loop(ctx context.Context) {
	const idleWait = 200 * time.Millisecond

	for {
		wait, reloadCh := w.nextWait(idleWait)
		switch waitForNext(ctx, wait, reloadCh) {
		case waitResultStop:
			return
		case waitResultReload:
			continue
		}

		for _, provider := range w.takeDueProviders() {
			if w.runner == nil {
				w.finishProviderRun(provider, LatestSnapshot{})
				continue
			}
			_, _ = runInspection(w.runner, ctx, RunRequest{
				TriggerType: TriggerTypeScheduled,
				Provider:    provider,
			}, func(snapshot LatestSnapshot) {
				w.finishProviderRun(provider, snapshot)
			})
		}
	}
}

func (w *Worker) finishProviderRun(provider string, snapshot LatestSnapshot) {
	provider = normalizeProvider(provider)
	w.mutex.Lock()
	delete(w.runningProviders, provider)
	w.mutex.Unlock()

	if snapshot.Settings.TargetType != "" {
		w.Reload(snapshot.Settings, snapshot.Run.NextTriggerAtMSByProvider)
		return
	}
	w.notifyReload()
}

func (w *Worker) load(settings InspectionSettings, nextTriggerAtMSByProvider map[string]int64) {
	settings = applyDefaultSettings(settings)
	nextTriggers := maps.Clone(nextTriggerAtMSByProvider)
	if nextTriggers == nil {
		nextTriggers = make(map[string]int64)
	}
	nowMS := time.Now().UnixMilli()
	enabled := false
	for provider, schedule := range settings.Schedules {
		provider = normalizeProvider(provider)
		if provider == "" || !schedule.Enabled || schedule.IntervalMinutes <= 0 {
			delete(nextTriggers, provider)
			continue
		}
		enabled = true
		if nextTriggers[provider] <= 0 {
			nextTriggers[provider] = nowMS + intervalMilliseconds(schedule.IntervalMinutes)
		}
	}
	for provider := range nextTriggers {
		schedule := settings.ScheduleFor(provider)
		if !schedule.Enabled || schedule.IntervalMinutes <= 0 {
			delete(nextTriggers, provider)
		}
	}

	w.mutex.Lock()
	w.settings = settings
	w.nextTriggerAtMSByProvider = nextTriggers
	w.enabled = enabled
	w.mutex.Unlock()
}

func (w *Worker) enabledProviders() []string {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	providers := make([]string, 0)
	for provider, schedule := range w.settings.Schedules {
		if schedule.Enabled && schedule.IntervalMinutes > 0 {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	return providers
}

func (w *Worker) nextWait(idleWait time.Duration) (time.Duration, <-chan struct{}) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	wait := idleWait
	if w.enabled {
		nowMS := time.Now().UnixMilli()
		var earliestMS int64
		for provider, nextTriggerAtMS := range w.nextTriggerAtMSByProvider {
			if _, running := w.runningProviders[provider]; running {
				continue
			}
			schedule := w.settings.ScheduleFor(provider)
			if !schedule.Enabled || schedule.IntervalMinutes <= 0 {
				continue
			}
			if earliestMS == 0 || nextTriggerAtMS < earliestMS {
				earliestMS = nextTriggerAtMS
			}
		}
		if earliestMS > 0 {
			wait = time.Duration(earliestMS-nowMS) * time.Millisecond
			if wait < 0 {
				wait = 0
			}
		}
	}
	return wait, w.reloadCh
}

func (w *Worker) takeDueProviders() []string {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	nowMS := time.Now().UnixMilli()
	providers := make([]string, 0)
	for provider, nextTriggerAtMS := range w.nextTriggerAtMSByProvider {
		if _, running := w.runningProviders[provider]; running {
			continue
		}
		schedule := w.settings.ScheduleFor(provider)
		if !schedule.Enabled || schedule.IntervalMinutes <= 0 || nextTriggerAtMS > nowMS {
			continue
		}
		providers = append(providers, provider)
		w.runningProviders[provider] = struct{}{}
		w.nextTriggerAtMSByProvider[provider] = nowMS + intervalMilliseconds(schedule.IntervalMinutes)
	}
	sort.Strings(providers)
	return providers
}

func (w *Worker) notifyReload() {
	if w == nil || w.reloadCh == nil {
		return
	}
	select {
	case w.reloadCh <- struct{}{}:
	default:
	}
}

func intervalMilliseconds(intervalMinutes int) int64 {
	return int64(time.Duration(intervalMinutes) * time.Minute / time.Millisecond)
}

type waitResult int

const (
	waitResultStop waitResult = iota
	waitResultTimer
	waitResultReload
)

func waitForNext(ctx context.Context, d time.Duration, reloadCh <-chan struct{}) waitResult {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return waitResultStop
	case <-timer.C:
		return waitResultTimer
	case <-reloadCh:
		return waitResultReload
	}
}
