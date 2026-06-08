package codexinspection

import (
	"context"
	"sync"
	"time"
)

type Runner interface {
	Run(ctx context.Context, req RunRequest) (LatestSnapshot, error)
}

type Worker struct {
	runner   Runner
	mutex    sync.Mutex
	settings InspectionSettings
	enabled  bool
	reloadCh chan struct{}
}

func NewWorker(runner Runner) *Worker {
	return &Worker{
		runner:   runner,
		reloadCh: make(chan struct{}, 1),
	}
}

func (w *Worker) Enabled() bool {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.enabled
}

func (w *Worker) Reload(settings InspectionSettings) {
	w.mutex.Lock()
	w.settings = settings
	w.enabled = settings.Schedule.Enabled && settings.Schedule.IntervalMinutes > 0
	w.mutex.Unlock()
	w.notifyReload()
}

func (w *Worker) TriggerNowForTest(ctx context.Context) {
	if w == nil || w.runner == nil {
		return
	}
	_, _ = w.runner.Run(ctx, RunRequest{TriggerType: TriggerTypeScheduled})
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
		select {
		case <-ctx.Done():
			return
		default:
		}

		w.mutex.Lock()
		enabled := w.enabled
		intervalMinutes := w.settings.Schedule.IntervalMinutes
		reloadCh := w.reloadCh
		w.mutex.Unlock()

		wait := idleWait
		if enabled && intervalMinutes > 0 {
			wait = time.Duration(intervalMinutes) * time.Minute
		}

		switch waitForNext(ctx, wait, reloadCh) {
		case waitResultStop:
			return
		case waitResultReload:
			continue
		}

		w.mutex.Lock()
		enabled = w.enabled
		w.mutex.Unlock()
		if !enabled {
			continue
		}
		if w.runner != nil {
			_, _ = w.runner.Run(ctx, RunRequest{TriggerType: TriggerTypeScheduled})
		}
	}
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
