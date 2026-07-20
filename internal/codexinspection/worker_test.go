package codexinspection

import (
	"context"
	"testing"
	"time"
)

func TestWorker_ReloadChangesInterval(t *testing.T) {
	runner := &recordingRunner{}
	worker := NewWorker(runner)

	settings := DefaultSettings()
	settings.SetSchedule("codex", InspectionSchedule{
		Enabled:         true,
		Mode:            "interval",
		IntervalMinutes: 5,
	})
	worker.Reload(settings, map[string]int64{"codex": time.Now().Add(5 * time.Minute).UnixMilli()})

	if !worker.Enabled() {
		t.Fatal("Enabled() = false, want true")
	}

	worker.TriggerNowForTest(context.Background())

	if len(runner.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(runner.requests))
	}
	if runner.requests[0].TriggerType != TriggerTypeScheduled {
		t.Fatalf("requests[0].TriggerType = %q, want %q", runner.requests[0].TriggerType, TriggerTypeScheduled)
	}
	if runner.requests[0].Provider != "codex" {
		t.Fatalf("requests[0].Provider = %q, want codex", runner.requests[0].Provider)
	}
}

func TestWorker_TriggerNowRunsEachEnabledProvider(t *testing.T) {
	runner := &recordingRunner{}
	worker := NewWorker(runner)
	settings := DefaultSettings()
	settings.SetSchedule("codex", InspectionSchedule{Enabled: true, Mode: "interval", IntervalMinutes: 5})
	settings.SetSchedule("claude", InspectionSchedule{Enabled: true, Mode: "interval", IntervalMinutes: 10})
	settings.SetSchedule("xai", InspectionSchedule{Enabled: false, Mode: "interval", IntervalMinutes: 15})
	worker.Reload(settings, nil)

	worker.TriggerNowForTest(context.Background())

	if len(runner.requests) != 2 {
		t.Fatalf("len(requests) = %d, want 2", len(runner.requests))
	}
	if runner.requests[0].Provider != "claude" || runner.requests[1].Provider != "codex" {
		t.Fatalf("providers = [%q %q], want [claude codex]", runner.requests[0].Provider, runner.requests[1].Provider)
	}
}

func TestWorker_TakesOnlyProvidersWhoseIndependentDeadlineIsDue(t *testing.T) {
	worker := NewWorker(&recordingRunner{})
	settings := DefaultSettings()
	settings.SetSchedule("codex", InspectionSchedule{Enabled: true, Mode: "interval", IntervalMinutes: 5})
	settings.SetSchedule("xai", InspectionSchedule{Enabled: true, Mode: "interval", IntervalMinutes: 10})
	nowMS := time.Now().UnixMilli()
	worker.Reload(settings, map[string]int64{
		"codex": nowMS + 60_000,
		"xai":   nowMS - 1,
	})

	dueProviders := worker.takeDueProviders()
	if len(dueProviders) != 1 || dueProviders[0] != "xai" {
		t.Fatalf("dueProviders = %v, want [xai]", dueProviders)
	}
	if worker.nextTriggerAtMSByProvider["codex"] != nowMS+60_000 {
		t.Fatalf("codex deadline changed to %d", worker.nextTriggerAtMSByProvider["codex"])
	}
	if worker.nextTriggerAtMSByProvider["xai"] <= nowMS {
		t.Fatalf("xai deadline = %d, want advanced", worker.nextTriggerAtMSByProvider["xai"])
	}
}

func TestWorker_ReloadWakesWaitingLoop(t *testing.T) {
	worker := NewWorker(&recordingRunner{})
	resultCh := make(chan waitResult, 1)

	go func() {
		resultCh <- waitForNext(context.Background(), time.Minute, worker.reloadCh)
	}()

	worker.Reload(InspectionSettings{}, nil)

	select {
	case result := <-resultCh:
		if result != waitResultReload {
			t.Fatalf("wait result = %d, want reload", result)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Reload did not wake waiting loop")
	}
}

func TestRunInspectionPrefersCompletionRunner(t *testing.T) {
	runner := &completionRecordingRunner{}
	completed := false
	if _, err := runInspection(runner, context.Background(), RunRequest{Provider: "xai"}, func(LatestSnapshot) {
		completed = true
	}); err != nil {
		t.Fatalf("runInspection() error = %v", err)
	}
	if runner.startCalls != 1 || runner.runCalls != 0 {
		t.Fatalf("calls = start:%d run:%d, want start:1 run:0", runner.startCalls, runner.runCalls)
	}
	if !completed {
		t.Fatal("completion callback was not called")
	}
}

func TestWorkerReloadsScheduleFromCompletedRun(t *testing.T) {
	runner := &controlledCompletionRunner{
		started: make(chan struct{}, 1),
		finish:  make(chan LatestSnapshot, 1),
	}
	worker := NewWorker(runner)
	settings := DefaultSettings()
	settings.SetSchedule("codex", InspectionSchedule{Enabled: true, Mode: "interval", IntervalMinutes: 5})
	worker.Reload(settings, map[string]int64{"codex": time.Now().Add(-time.Second).UnixMilli()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("scheduled run did not start")
	}
	worker.mutex.Lock()
	worker.nextTriggerAtMSByProvider["codex"] = time.Now().Add(-time.Second).UnixMilli()
	worker.mutex.Unlock()
	if due := worker.takeDueProviders(); len(due) != 0 {
		t.Fatalf("due providers while running = %v, want none", due)
	}

	finalNext := time.Now().Add(10 * time.Minute).UnixMilli()
	finished := DefaultSnapshot()
	finished.Settings = settings
	finished.Run.NextTriggerAtMSByProvider = map[string]int64{"codex": finalNext}
	runner.finish <- finished

	deadline := time.Now().Add(time.Second)
	for {
		worker.mutex.Lock()
		next := worker.nextTriggerAtMSByProvider["codex"]
		_, running := worker.runningProviders["codex"]
		worker.mutex.Unlock()
		if next == finalNext && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker state = next:%d running:%v, want next:%d running:false", next, running, finalNext)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type controlledCompletionRunner struct {
	started chan struct{}
	finish  chan LatestSnapshot
}

func (r *controlledCompletionRunner) StartRunWithCompletion(_ context.Context, _ RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error) {
	r.started <- struct{}{}
	go func() {
		onFinish(<-r.finish)
	}()
	return DefaultSnapshot(), nil
}

func (r *controlledCompletionRunner) Run(context.Context, RunRequest) (LatestSnapshot, error) {
	return DefaultSnapshot(), nil
}

type completionRecordingRunner struct {
	startCalls int
	runCalls   int
}

func (r *completionRecordingRunner) StartRunWithCompletion(_ context.Context, _ RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error) {
	r.startCalls++
	snapshot := DefaultSnapshot()
	onFinish(snapshot)
	return snapshot, nil
}

func (r *completionRecordingRunner) Run(context.Context, RunRequest) (LatestSnapshot, error) {
	r.runCalls++
	return DefaultSnapshot(), nil
}

type recordingRunner struct {
	requests   []RunRequest
	runStarted chan context.Context
}

func (r *recordingRunner) Run(ctx context.Context, req RunRequest) (LatestSnapshot, error) {
	r.requests = append(r.requests, req)
	if r.runStarted != nil {
		select {
		case r.runStarted <- ctx:
		default:
		}
	}
	return DefaultSnapshot(), nil
}
