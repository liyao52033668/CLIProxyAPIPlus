package codexinspection

import (
	"context"
	"testing"
	"time"
)

func TestWorker_ReloadChangesInterval(t *testing.T) {
	runner := &recordingRunner{}
	worker := NewWorker(runner)

	worker.Reload(InspectionSettings{
		Schedule: InspectionSchedule{
			Enabled:         true,
			IntervalMinutes: 5,
		},
	})

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
}

func TestWorker_ReloadWakesWaitingLoop(t *testing.T) {
	worker := NewWorker(&recordingRunner{})
	resultCh := make(chan waitResult, 1)

	go func() {
		resultCh <- waitForNext(context.Background(), time.Minute, worker.reloadCh)
	}()

	worker.Reload(InspectionSettings{})

	select {
	case result := <-resultCh:
		if result != waitResultReload {
			t.Fatalf("wait result = %d, want reload", result)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Reload did not wake waiting loop")
	}
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
