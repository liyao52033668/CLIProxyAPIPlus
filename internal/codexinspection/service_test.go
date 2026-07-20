package codexinspection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestService_RunStoresLatestSnapshot(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{
		files: []AuthFileRecord{
			{
				FileName:    "alpha.json",
				DisplayName: "Alpha",
				Provider:    "codex",
				AuthIndex:   "0",
				AccountID:   "acct-1",
			},
		},
	}
	prober := &fakeProber{
		results: []InspectionResultItem{
			{
				FileName:     "alpha.json",
				DisplayName:  "Alpha",
				Provider:     "codex",
				AuthIndex:    "0",
				AccountID:    "acct-1",
				Action:       ActionDisable,
				ActionReason: "quota exhausted",
				Executable:   true,
			},
		},
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if snapshot.Run.Status != RunStatusCompleted {
		t.Fatalf("Run().Run.Status = %q, want %q", snapshot.Run.Status, RunStatusCompleted)
	}
	if snapshot.Run.TriggerType != TriggerTypeManual {
		t.Fatalf("Run().Run.TriggerType = %q, want %q", snapshot.Run.TriggerType, TriggerTypeManual)
	}
	if snapshot.Run.StartedAtMS <= 0 {
		t.Fatalf("Run().Run.StartedAtMS = %d, want > 0", snapshot.Run.StartedAtMS)
	}
	if snapshot.Run.FinishedAtMS < snapshot.Run.StartedAtMS {
		t.Fatalf("Run().Run.FinishedAtMS = %d, want >= %d", snapshot.Run.FinishedAtMS, snapshot.Run.StartedAtMS)
	}
	if len(snapshot.Results) != 1 {
		t.Fatalf("len(Run().Results) = %d, want 1", len(snapshot.Results))
	}
	if snapshot.Results[0].Action != ActionDisable {
		t.Fatalf("Run().Results[0].Action = %q, want %q", snapshot.Results[0].Action, ActionDisable)
	}
	if snapshot.Run.Summary.DisableCount != 1 {
		t.Fatalf("Run().Run.Summary.DisableCount = %d, want 1", snapshot.Run.Summary.DisableCount)
	}
	if repo.saved.Run.Status != RunStatusCompleted {
		t.Fatalf("saved snapshot status = %q, want %q", repo.saved.Run.Status, RunStatusCompleted)
	}
	if len(repo.saved.Results) != 1 {
		t.Fatalf("len(saved snapshot results) = %d, want 1", len(repo.saved.Results))
	}
}

func TestService_RunSelectsProviderAndClearsPreviousResults(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: DefaultSettings(),
		Results:  []InspectionResultItem{{FileName: "codex.json", Provider: "codex"}},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "claude.json", Provider: "claude"}}}
	prober := &fakeProber{results: []InspectionResultItem{{FileName: "claude.json", Provider: "claude", Action: ActionKeep}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{
		TriggerType: TriggerTypeManual,
		Provider:    " Claude ",
		FileNames:   []string{"claude.json"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gateway.receivedProvider != "claude" {
		t.Fatalf("gateway provider = %q, want claude", gateway.receivedProvider)
	}
	if snapshot.Settings.TargetType != "claude" {
		t.Fatalf("TargetType = %q, want claude", snapshot.Settings.TargetType)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Provider != "claude" {
		t.Fatalf("Results = %+v, want only claude result", snapshot.Results)
	}
}

func TestService_RunRejectsConcurrentRun(t *testing.T) {
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, &fakeGateway{}, &fakeProber{})
	service.activeProviders["codex"] = struct{}{}

	_, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("Run() error = %v, want %v", err, ErrRunAlreadyActive)
	}
}

func TestService_RunPersistsRunningStateBeforeProbeCompletes(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{
		files: []AuthFileRecord{{
			FileName:    "alpha.json",
			DisplayName: "Alpha",
			Provider:    "codex",
			AuthIndex:   "0",
			AccountID:   "acct-1",
		}},
	}
	prober := &fakeProber{
		results: []InspectionResultItem{{
			FileName:     "alpha.json",
			DisplayName:  "Alpha",
			Action:       ActionKeep,
			ActionReason: "no issue detected",
		}},
	}
	service := NewService(repo, gateway, prober)

	_, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(repo.saves) < 2 {
		t.Fatalf("len(repo.saves) = %d, want at least 2", len(repo.saves))
	}
	if repo.saves[0].Run.Status != RunStatusRunning {
		t.Fatalf("first saved status = %q, want %q", repo.saves[0].Run.Status, RunStatusRunning)
	}
	if repo.saves[0].Run.TriggerType != TriggerTypeScheduled {
		t.Fatalf("first saved trigger = %q, want %q", repo.saves[0].Run.TriggerType, TriggerTypeScheduled)
	}
	if repo.saves[len(repo.saves)-1].Run.Status != RunStatusCompleted {
		t.Fatalf("last saved status = %q, want %q", repo.saves[len(repo.saves)-1].Run.Status, RunStatusCompleted)
	}
}

func TestService_ExecuteActionsRequiresDeleteConfirmation(t *testing.T) {
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, &fakeGateway{}, &fakeProber{})

	_, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionDelete,
		FileNames: []string{"alpha.json"},
	})
	if !errors.Is(err, ErrDeleteConfirmationRequired) {
		t.Fatalf("ExecuteActions() error = %v, want %v", err, ErrDeleteConfirmationRequired)
	}
}

func TestService_ExecuteActionsCallsGatewayAndKeepsGoingOnItemFailure(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{Results: []InspectionResultItem{{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false}, {FileName: "beta.json", DisplayName: "Beta", Disabled: false}, {FileName: "gamma.json", DisplayName: "Gamma", Disabled: false}}}}
	gateway := &fakeGateway{
		setDisabledErrors: map[string]error{"beta.json": errors.New("disable failed")},
		deleteErrors:      map[string]error{"gamma.json": errors.New("delete failed")},
	}
	service := NewService(repo, gateway, &fakeProber{})

	disableResult, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionDisable,
		FileNames: []string{"alpha.json", "beta.json"},
	})
	if err != nil {
		t.Fatalf("ExecuteActions(disable) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 2 {
		t.Fatalf("len(setDisabledCalls) = %d, want 2", len(gateway.setDisabledCalls))
	}
	if gateway.setDisabledCalls[0] != (setDisabledCall{name: "alpha.json", disabled: true}) {
		t.Fatalf("first setDisabled call = %+v, want alpha disable", gateway.setDisabledCalls[0])
	}
	if gateway.setDisabledCalls[1] != (setDisabledCall{name: "beta.json", disabled: true}) {
		t.Fatalf("second setDisabled call = %+v, want beta disable", gateway.setDisabledCalls[1])
	}
	if len(disableResult.Logs) != 2 {
		t.Fatalf("len(disable logs) = %d, want 2", len(disableResult.Logs))
	}
	if !disableResult.Logs[0].Success {
		t.Fatalf("disable logs[0].Success = false, want true")
	}
	if disableResult.Logs[1].Success {
		t.Fatalf("disable logs[1].Success = true, want false")
	}
	if len(disableResult.Snapshot.Results) != 3 {
		t.Fatalf("len(disable snapshot results) = %d, want 3", len(disableResult.Snapshot.Results))
	}
	if !disableResult.Snapshot.Results[0].Disabled {
		t.Fatal("disable snapshot alpha disabled = false, want true")
	}
	if disableResult.Snapshot.Results[1].Disabled {
		t.Fatal("disable snapshot beta disabled = true, want false")
	}
	if !repo.saved.Results[0].Disabled {
		t.Fatal("saved alpha disabled = false, want true")
	}
	if repo.saved.Results[1].Disabled {
		t.Fatal("saved beta disabled = true, want false")
	}

	deleteResult, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:        ActionDelete,
		FileNames:     []string{"gamma.json"},
		ConfirmDelete: true,
	})
	if err != nil {
		t.Fatalf("ExecuteActions(delete) error = %v", err)
	}
	if len(gateway.deleteCalls) != 1 {
		t.Fatalf("len(deleteCalls) = %d, want 1", len(gateway.deleteCalls))
	}
	if len(gateway.deleteCalls[0]) != 1 || gateway.deleteCalls[0][0] != "gamma.json" {
		t.Fatalf("delete call = %#v, want []string{\"gamma.json\"}", gateway.deleteCalls[0])
	}
	if len(deleteResult.Logs) != 1 {
		t.Fatalf("len(delete logs) = %d, want 1", len(deleteResult.Logs))
	}
	if deleteResult.Logs[0].Success {
		t.Fatalf("delete logs[0].Success = true, want false")
	}
	if len(deleteResult.Snapshot.Results) != 3 {
		t.Fatalf("len(delete snapshot results) = %d, want 3", len(deleteResult.Snapshot.Results))
	}
	if len(repo.saved.ActionLogs) != 1 {
		t.Fatalf("len(saved action logs) = %d, want 1", len(repo.saved.ActionLogs))
	}

	enableResult, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionEnable,
		FileNames: []string{"alpha.json"},
	})
	if err != nil {
		t.Fatalf("ExecuteActions(enable) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 3 {
		t.Fatalf("len(setDisabledCalls) after enable = %d, want 3", len(gateway.setDisabledCalls))
	}
	if gateway.setDisabledCalls[2] != (setDisabledCall{name: "alpha.json", disabled: false}) {
		t.Fatalf("third setDisabled call = %+v, want alpha enable", gateway.setDisabledCalls[2])
	}
	if len(enableResult.Logs) != 1 {
		t.Fatalf("len(enable logs) = %d, want 1", len(enableResult.Logs))
	}
	if enableResult.Snapshot.Results[0].Disabled {
		t.Fatal("enable snapshot alpha disabled = true, want false")
	}
	if repo.saved.Results[0].Disabled {
		t.Fatal("saved alpha after enable disabled = true, want false")
	}
}

func TestService_ExecuteActionsClearsXAIAutoDisabledState(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		Results: []InspectionResultItem{{
			FileName: "grok.json",
			Provider: "xai",
			Disabled: true,
			Action:   ActionKeep,
		}},
		AutoDisabledFiles: map[string]map[string]bool{
			"xai": {"grok.json": true},
		},
	}}
	service := NewService(repo, &fakeGateway{}, &fakeProber{})

	result, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionDisable,
		FileNames: []string{"grok.json"},
	})
	if err != nil {
		t.Fatalf("ExecuteActions(disable) error = %v", err)
	}
	if result.Snapshot.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("AutoDisabledFiles[xai][grok.json] = true, want false after manual action")
	}
	if !result.Snapshot.Results[0].Disabled {
		t.Fatal("Results[0].Disabled = false, want true")
	}
}

func TestService_ExecuteActionsDeletesSuccessfulItemsAndPreservesFailures(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{Results: []InspectionResultItem{{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false}, {FileName: "beta.json", DisplayName: "Beta", Disabled: false}, {FileName: "gamma.json", DisplayName: "Gamma", Disabled: false}}}}
	gateway := &fakeGateway{
		deleteErrors: map[string]error{"beta.json": errors.New("delete failed")},
	}
	service := NewService(repo, gateway, &fakeProber{})

	result, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:        ActionDelete,
		FileNames:     []string{"alpha.json", "beta.json"},
		ConfirmDelete: true,
	})
	if err != nil {
		t.Fatalf("ExecuteActions(delete mixed) error = %v", err)
	}
	if len(result.Logs) != 2 {
		t.Fatalf("len(result.Logs) = %d, want 2", len(result.Logs))
	}
	if !result.Logs[0].Success {
		t.Fatal("delete logs[0].Success = false, want true")
	}
	if result.Logs[1].Success {
		t.Fatal("delete logs[1].Success = true, want false")
	}
	if len(result.Snapshot.Results) != 2 {
		t.Fatalf("len(result.Snapshot.Results) = %d, want 2", len(result.Snapshot.Results))
	}
	if result.Snapshot.Results[0].FileName != "beta.json" {
		t.Fatalf("result.Snapshot.Results[0].FileName = %q, want %q", result.Snapshot.Results[0].FileName, "beta.json")
	}
	if result.Snapshot.Results[1].FileName != "gamma.json" {
		t.Fatalf("result.Snapshot.Results[1].FileName = %q, want %q", result.Snapshot.Results[1].FileName, "gamma.json")
	}
	if len(repo.saved.Results) != 2 {
		t.Fatalf("len(saved.Results) = %d, want 2", len(repo.saved.Results))
	}
	if repo.saved.Results[0].FileName != "beta.json" {
		t.Fatalf("saved.Results[0].FileName = %q, want %q", repo.saved.Results[0].FileName, "beta.json")
	}
	if repo.saved.Results[1].FileName != "gamma.json" {
		t.Fatalf("saved.Results[1].FileName = %q, want %q", repo.saved.Results[1].FileName, "gamma.json")
	}
}

func TestService_ExecuteActionsRebuildsSummaryAfterMutation(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{
			TotalFiles:   2,
			SampledCount: 2,
			DisableCount: 1,
			EnabledCount: 2,
		}},
		Results: []InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: ActionDisable, Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: ActionKeep, Disabled: false},
		},
	}}
	service := NewService(repo, &fakeGateway{}, &fakeProber{})

	result, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionDisable,
		FileNames: []string{"alpha.json"},
	})
	if err != nil {
		t.Fatalf("ExecuteActions(disable) error = %v", err)
	}

	summary := result.Snapshot.Run.Summary
	if summary.TotalFiles != 2 {
		t.Fatalf("Summary.TotalFiles = %d, want 2", summary.TotalFiles)
	}
	if summary.SampledCount != 2 {
		t.Fatalf("Summary.SampledCount = %d, want 2", summary.SampledCount)
	}
	if summary.DisableCount != 0 {
		t.Fatalf("Summary.DisableCount = %d, want 0", summary.DisableCount)
	}
	if summary.DisabledCount != 1 {
		t.Fatalf("Summary.DisabledCount = %d, want 1", summary.DisabledCount)
	}
	if summary.EnabledCount != 1 {
		t.Fatalf("Summary.EnabledCount = %d, want 1", summary.EnabledCount)
	}
	if repo.saved.Run.Summary.DisabledCount != 1 {
		t.Fatalf("saved Summary.DisabledCount = %d, want 1", repo.saved.Run.Summary.DisabledCount)
	}
}

func TestService_ExecuteActionsClearsResolvedActionState(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{TotalFiles: 1, SampledCount: 1, DisableCount: 1, EnabledCount: 1}},
		Results: []InspectionResultItem{{
			FileName:     "alpha.json",
			DisplayName:  "Alpha",
			Action:       ActionDisable,
			ActionReason: "usedPercent >= 85",
			Disabled:     false,
		}},
	}}
	service := NewService(repo, &fakeGateway{}, &fakeProber{})

	result, err := service.ExecuteActions(context.Background(), ExecuteActionsRequest{
		Action:    ActionDisable,
		FileNames: []string{"alpha.json"},
	})
	if err != nil {
		t.Fatalf("ExecuteActions(disable) error = %v", err)
	}
	if result.Snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("result action = %q, want %q", result.Snapshot.Results[0].Action, ActionKeep)
	}
	if result.Snapshot.Results[0].ActionReason != "no issue detected" {
		t.Fatalf("result action reason = %q, want no issue detected", result.Snapshot.Results[0].ActionReason)
	}
}

func TestService_GetSnapshotReconcilesMissingAuthFiles(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{
			TotalFiles:    3,
			SampledCount:  3,
			KeepCount:     3,
			DisabledCount: 1,
			EnabledCount:  2,
		}},
		Results: []InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: ActionKeep, Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: ActionKeep, Disabled: true},
			{FileName: "gamma.json", DisplayName: "Gamma", Action: ActionKeep, Disabled: false},
		},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{
		{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false},
		{FileName: "beta.json", DisplayName: "Beta", Disabled: false},
	}}
	service := NewService(repo, gateway, &fakeProber{})

	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if len(snapshot.Results) != 2 {
		t.Fatalf("len(snapshot.Results) = %d, want 2", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "alpha.json" || snapshot.Results[1].FileName != "beta.json" {
		t.Fatalf("snapshot.Results = %#v, want alpha and beta", snapshot.Results)
	}
	if snapshot.Results[1].Disabled {
		t.Fatal("snapshot beta Disabled = true, want false")
	}
	if snapshot.Run.Summary.TotalFiles != 2 {
		t.Fatalf("Summary.TotalFiles = %d, want 2", snapshot.Run.Summary.TotalFiles)
	}
	if snapshot.Run.Summary.SampledCount != 2 {
		t.Fatalf("Summary.SampledCount = %d, want 2", snapshot.Run.Summary.SampledCount)
	}
	if snapshot.Run.Summary.KeepCount != 2 {
		t.Fatalf("Summary.KeepCount = %d, want 2", snapshot.Run.Summary.KeepCount)
	}
	if snapshot.Run.Summary.DisabledCount != 0 {
		t.Fatalf("Summary.DisabledCount = %d, want 0", snapshot.Run.Summary.DisabledCount)
	}
	if snapshot.Run.Summary.EnabledCount != 2 {
		t.Fatalf("Summary.EnabledCount = %d, want 2", snapshot.Run.Summary.EnabledCount)
	}
	if len(repo.saves) != 1 {
		t.Fatalf("len(repo.saves) = %d, want 1", len(repo.saves))
	}
}

func TestService_GetSnapshotClearsXAIAutoDisabledStateAfterExternalEnable(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		Results: []InspectionResultItem{{
			FileName: "grok.json",
			Provider: "xai",
			Disabled: true,
			Action:   ActionKeep,
		}},
		AutoDisabledFiles: map[string]map[string]bool{
			"xai": {"grok.json": true},
		},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "grok.json", Provider: "xai", Disabled: false}}}
	service := NewService(repo, gateway, &fakeProber{})

	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("AutoDisabledFiles[xai][grok.json] = true, want false")
	}
	if snapshot.Results[0].Disabled {
		t.Fatal("Results[0].Disabled = true, want false")
	}
}

func TestService_GetSnapshotClearsResolvedDisableRecommendation(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{TotalFiles: 1, SampledCount: 1, DisableCount: 1, EnabledCount: 1}},
		Results: []InspectionResultItem{{
			FileName:     "alpha.json",
			DisplayName:  "Alpha",
			Action:       ActionDisable,
			ActionReason: "usedPercent >= 85",
			Disabled:     false,
		}},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "alpha.json", DisplayName: "Alpha", Disabled: true}}}
	service := NewService(repo, gateway, &fakeProber{})

	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("snapshot action = %q, want %q", snapshot.Results[0].Action, ActionKeep)
	}
	if snapshot.Results[0].ActionReason != "no issue detected" {
		t.Fatalf("snapshot action reason = %q, want no issue detected", snapshot.Results[0].ActionReason)
	}
	if snapshot.Run.Summary.DisableCount != 0 {
		t.Fatalf("Summary.DisableCount = %d, want 0", snapshot.Run.Summary.DisableCount)
	}
	if snapshot.Run.Summary.DisabledCount != 1 {
		t.Fatalf("Summary.DisabledCount = %d, want 1", snapshot.Run.Summary.DisabledCount)
	}
}

func TestService_RunScheduledAutoAppliesSuggestedActions(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{}
	prober := &fakeProber{
		results: []InspectionResultItem{
			{
				FileName:     "expired.json",
				DisplayName:  "Expired",
				Action:       ActionDelete,
				ActionReason: "401 response",
			},
			{
				FileName:     "weekly-low.json",
				DisplayName:  "Weekly Low",
				Action:       ActionDelete,
				ActionReason: "weeklyUsedPercent >= 85",
			},
			{
				FileName:     "five-hour-low.json",
				DisplayName:  "Five Hour Low",
				Action:       ActionDisable,
				ActionReason: "fiveHourUsedPercent >= 85",
				Disabled:     false,
			},
			{
				FileName:     "recovered.json",
				DisplayName:  "Recovered",
				Action:       ActionEnable,
				ActionReason: "fiveHourUsedPercent < 85 && weeklyUsedPercent < 85",
				Disabled:     true,
			},
			{
				FileName:     "healthy.json",
				DisplayName:  "Healthy",
				Action:       ActionKeep,
				ActionReason: "no issue detected",
			},
			{
				FileName:     "failed.json",
				DisplayName:  "Failed",
				Action:       ActionFailed,
				ActionReason: "refresh failed",
			},
		},
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run(scheduled) error = %v", err)
	}
	if len(gateway.deleteCalls) != 2 {
		t.Fatalf("len(deleteCalls) = %d, want 2", len(gateway.deleteCalls))
	}
	if len(gateway.setDisabledCalls) != 2 {
		t.Fatalf("len(setDisabledCalls) = %d, want 2", len(gateway.setDisabledCalls))
	}
	if gateway.setDisabledCalls[0] != (setDisabledCall{name: "five-hour-low.json", disabled: true}) {
		t.Fatalf("setDisabledCalls[0] = %+v, want five-hour-low disable", gateway.setDisabledCalls[0])
	}
	if gateway.setDisabledCalls[1] != (setDisabledCall{name: "recovered.json", disabled: false}) {
		t.Fatalf("setDisabledCalls[1] = %+v, want recovered enable", gateway.setDisabledCalls[1])
	}
	if len(snapshot.Results) != 4 {
		t.Fatalf("len(snapshot.Results) = %d, want 4", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "five-hour-low.json" || !snapshot.Results[0].Disabled || snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("snapshot.Results[0] = %+v, want disabled keep", snapshot.Results[0])
	}
	if snapshot.Results[1].FileName != "recovered.json" || snapshot.Results[1].Disabled || snapshot.Results[1].Action != ActionKeep {
		t.Fatalf("snapshot.Results[1] = %+v, want enabled keep", snapshot.Results[1])
	}
	if snapshot.Results[2].FileName != "healthy.json" {
		t.Fatalf("snapshot.Results[2].FileName = %q, want healthy.json", snapshot.Results[2].FileName)
	}
	if snapshot.Results[3].FileName != "failed.json" || snapshot.Results[3].Action != ActionFailed {
		t.Fatalf("snapshot.Results[3] = %+v, want failed result", snapshot.Results[3])
	}
	if snapshot.Run.Summary.FailedCount != 1 {
		t.Fatalf("FailedCount = %d, want 1", snapshot.Run.Summary.FailedCount)
	}
	if snapshot.Run.Summary.AutoDeletedCount != 2 {
		t.Fatalf("AutoDeletedCount = %d, want 2", snapshot.Run.Summary.AutoDeletedCount)
	}
	if len(snapshot.ActionLogs) != 4 {
		t.Fatalf("len(ActionLogs) = %d, want 4", len(snapshot.ActionLogs))
	}
	for _, log := range snapshot.ActionLogs {
		if !log.Success {
			t.Fatalf("ActionLog = %+v, want success", log)
		}
	}
}

func TestService_RunScheduledRestoresAutomaticallyDisabledXAIAccount(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{Settings: settings}}
	gateway := &fakeGateway{files: []AuthFileRecord{{
		FileName:    "grok.json",
		DisplayName: "Grok",
		Provider:    "xai",
		Disabled:    false,
	}}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "grok.json",
		DisplayName:  "Grok",
		Provider:     "xai",
		Action:       ActionDisable,
		ActionReason: "xAI free usage exhausted",
		Executable:   true,
	}}}
	service := NewService(repo, gateway, prober)

	first, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("first Run(scheduled) error = %v", err)
	}
	if !first.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("first AutoDisabledFiles[xai][grok.json] = false, want true")
	}
	if len(first.Results) != 1 || !first.Results[0].Disabled {
		t.Fatalf("first Results = %+v, want disabled Grok account", first.Results)
	}

	gateway.files[0].Disabled = true
	prober.results = []InspectionResultItem{{
		FileName:     "grok.json",
		DisplayName:  "Grok",
		Provider:     "xai",
		Disabled:     true,
		Action:       ActionKeep,
		ActionReason: "xAI probe succeeded; account remains disabled",
		Executable:   false,
	}}
	second, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("second Run(scheduled) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 2 {
		t.Fatalf("len(setDisabledCalls) = %d, want 2", len(gateway.setDisabledCalls))
	}
	if gateway.setDisabledCalls[1] != (setDisabledCall{name: "grok.json", disabled: false}) {
		t.Fatalf("setDisabledCalls[1] = %+v, want Grok enable", gateway.setDisabledCalls[1])
	}
	if len(second.Results) != 1 || second.Results[0].Disabled || second.Results[0].Action != ActionKeep {
		t.Fatalf("second Results = %+v, want enabled keep", second.Results)
	}
	if second.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("second AutoDisabledFiles[xai][grok.json] = true, want false")
	}
}

func TestService_RunScheduledEnablesHealthyDisabledXAIAccount(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{Settings: settings}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "grok.json", Provider: "xai", Disabled: true}}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "grok.json",
		Provider:     "xai",
		Disabled:     true,
		Action:       ActionKeep,
		ActionReason: "xAI probe succeeded; account remains disabled",
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run(scheduled) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 1 || gateway.setDisabledCalls[0] != (setDisabledCall{name: "grok.json", disabled: false}) {
		t.Fatalf("setDisabledCalls = %+v, want Grok enable", gateway.setDisabledCalls)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Disabled || snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("Results = %+v, want enabled keep", snapshot.Results)
	}
}

func TestService_RunScheduledPreservesXAIRecoveryWhenEnableFails(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		AutoDisabledFiles: map[string]map[string]bool{
			"xai": {"grok.json": true},
		},
	}}
	gateway := &fakeGateway{
		files:             []AuthFileRecord{{FileName: "grok.json", Provider: "xai", Disabled: true}},
		setDisabledErrors: map[string]error{"grok.json": errors.New("enable failed")},
	}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "grok.json",
		Provider:     "xai",
		Disabled:     true,
		Action:       ActionKeep,
		ActionReason: "xAI probe succeeded; account remains disabled",
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run(scheduled) error = %v", err)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Action != ActionEnable || !snapshot.Results[0].Disabled {
		t.Fatalf("Results = %+v, want retryable enable action", snapshot.Results)
	}
	if !snapshot.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("AutoDisabledFiles[xai][grok.json] = false, want true")
	}
	if len(snapshot.ActionLogs) != 1 || snapshot.ActionLogs[0].Success {
		t.Fatalf("ActionLogs = %+v, want failed enable log", snapshot.ActionLogs)
	}
}

func TestService_RunScheduledRequiresSuccessfulXAIProbeForRecovery(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		AutoDisabledFiles: map[string]map[string]bool{
			"xai": {"grok.json": true},
		},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "grok.json", Provider: "xai", Disabled: true}}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "grok.json",
		Provider:     "xai",
		Disabled:     true,
		Action:       ActionKeep,
		ActionReason: "no issue detected",
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run(scheduled) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 0 {
		t.Fatalf("len(setDisabledCalls) = %d, want 0", len(gateway.setDisabledCalls))
	}
	if !snapshot.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("AutoDisabledFiles[xai][grok.json] = false, want true")
	}
}

func TestService_RunProviderSwitchPreservesXAIAutoDisabledState(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "codex"
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		AutoDisabledFiles: map[string]map[string]bool{
			"xai": {"grok.json": true},
		},
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "claude.json", Provider: "claude"}}}
	prober := &fakeProber{results: []InspectionResultItem{{FileName: "claude.json", Provider: "claude", Action: ActionKeep}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual, Provider: "claude"})
	if err != nil {
		t.Fatalf("Run(provider switch) error = %v", err)
	}
	if !snapshot.AutoDisabledFiles["xai"]["grok.json"] {
		t.Fatal("AutoDisabledFiles[xai][grok.json] = false, want preserved true")
	}
}

func TestService_RunManualAutoEnablesHealthyDisabledXAIAccount(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetType = "xai"
	repo := &fakeRepository{snapshot: LatestSnapshot{Settings: settings}}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "grok.json", Provider: "xai", Disabled: true}}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "grok.json",
		Provider:     "xai",
		Disabled:     true,
		Action:       ActionEnable,
		ActionReason: XAIProbeSucceededReason,
		Executable:   true,
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("Run(manual) error = %v", err)
	}
	if len(gateway.setDisabledCalls) != 1 || gateway.setDisabledCalls[0] != (setDisabledCall{name: "grok.json", disabled: false}) {
		t.Fatalf("setDisabledCalls = %+v, want Grok enable", gateway.setDisabledCalls)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Disabled || snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("Results = %+v, want enabled keep", snapshot.Results)
	}
	if len(snapshot.ActionLogs) != 1 || !snapshot.ActionLogs[0].Success || snapshot.ActionLogs[0].Action != ActionEnable {
		t.Fatalf("ActionLogs = %+v, want successful enable log", snapshot.ActionLogs)
	}
}

func TestService_RunManualDoesNotAutoApplySuggestedActions(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{}
	prober := &fakeProber{
		results: []InspectionResultItem{
			{
				FileName:     "expired.json",
				DisplayName:  "Expired",
				Action:       ActionDelete,
				ActionReason: "401 response",
			},
			{
				FileName:     "five-hour-low.json",
				DisplayName:  "Five Hour Low",
				Action:       ActionDisable,
				ActionReason: "fiveHourUsedPercent >= 85",
			},
		},
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("Run(manual) error = %v", err)
	}
	if len(gateway.deleteCalls) != 0 {
		t.Fatalf("len(deleteCalls) = %d, want 0", len(gateway.deleteCalls))
	}
	if len(gateway.setDisabledCalls) != 0 {
		t.Fatalf("len(setDisabledCalls) = %d, want 0", len(gateway.setDisabledCalls))
	}
	if len(snapshot.Results) != 2 {
		t.Fatalf("len(snapshot.Results) = %d, want 2", len(snapshot.Results))
	}
	if snapshot.Run.Summary.AutoDeletedCount != 0 {
		t.Fatalf("AutoDeletedCount = %d, want 0", snapshot.Run.Summary.AutoDeletedCount)
	}
	if snapshot.Run.Summary.DeleteCount != 1 {
		t.Fatalf("DeleteCount = %d, want 1", snapshot.Run.Summary.DeleteCount)
	}
	if snapshot.Run.Summary.DisableCount != 1 {
		t.Fatalf("DisableCount = %d, want 1", snapshot.Run.Summary.DisableCount)
	}
	if snapshot.Run.Summary.ReauthCount != 0 {
		t.Fatalf("ReauthCount = %d, want 0", snapshot.Run.Summary.ReauthCount)
	}
}

func TestService_RunPartialMergesResultsAndRebuildsSummary(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{
			TotalFiles:    3,
			SampledCount:  3,
			KeepCount:     1,
			DisableCount:  1,
			DeleteCount:   1,
			DisabledCount: 1,
			EnabledCount:  2,
		}},
		Results: []InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: ActionKeep, ActionReason: "no issue detected", Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: ActionDisable, ActionReason: "quota exhausted", Disabled: false},
			{FileName: "gamma.json", DisplayName: "Gamma", Action: ActionDelete, ActionReason: "401 response", Disabled: true},
		},
		Settings: DefaultSettings(),
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{
		{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false},
		{FileName: "beta.json", DisplayName: "Beta", Disabled: false},
		{FileName: "gamma.json", DisplayName: "Gamma", Disabled: true},
	}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "beta.json",
		DisplayName:  "Beta",
		Action:       ActionEnable,
		ActionReason: "account recovered",
		Disabled:     true,
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{
		TriggerType: TriggerTypeManual,
		FileNames:   []string{"beta.json", "missing.json"},
	})
	if err != nil {
		t.Fatalf("Run(partial) error = %v", err)
	}
	if len(prober.receivedFiles) != 1 {
		t.Fatalf("len(receivedFiles) = %d, want 1", len(prober.receivedFiles))
	}
	if prober.receivedFiles[0].FileName != "beta.json" {
		t.Fatalf("receivedFiles[0].FileName = %q, want beta.json", prober.receivedFiles[0].FileName)
	}
	if len(snapshot.Results) != 3 {
		t.Fatalf("len(snapshot.Results) = %d, want 3", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "alpha.json" || snapshot.Results[0].Action != ActionKeep {
		t.Fatalf("snapshot.Results[0] = %+v, want alpha keep", snapshot.Results[0])
	}
	if snapshot.Results[1].FileName != "beta.json" || snapshot.Results[1].Action != ActionEnable {
		t.Fatalf("snapshot.Results[1] = %+v, want beta enable", snapshot.Results[1])
	}
	if snapshot.Results[2].FileName != "gamma.json" || snapshot.Results[2].Action != ActionDelete {
		t.Fatalf("snapshot.Results[2] = %+v, want gamma delete", snapshot.Results[2])
	}
	if snapshot.Run.Summary.TotalFiles != 3 {
		t.Fatalf("Summary.TotalFiles = %d, want 3", snapshot.Run.Summary.TotalFiles)
	}
	if snapshot.Run.Summary.SampledCount != 3 {
		t.Fatalf("Summary.SampledCount = %d, want 3", snapshot.Run.Summary.SampledCount)
	}
	if snapshot.Run.Summary.KeepCount != 1 {
		t.Fatalf("Summary.KeepCount = %d, want 1", snapshot.Run.Summary.KeepCount)
	}
	if snapshot.Run.Summary.DeleteCount != 1 {
		t.Fatalf("Summary.DeleteCount = %d, want 1", snapshot.Run.Summary.DeleteCount)
	}
	if snapshot.Run.Summary.EnableCount != 1 {
		t.Fatalf("Summary.EnableCount = %d, want 1", snapshot.Run.Summary.EnableCount)
	}
	if snapshot.Run.Summary.DisableCount != 0 {
		t.Fatalf("Summary.DisableCount = %d, want 0", snapshot.Run.Summary.DisableCount)
	}
	if snapshot.Run.Summary.DisabledCount != 2 {
		t.Fatalf("Summary.DisabledCount = %d, want 2", snapshot.Run.Summary.DisabledCount)
	}
	if snapshot.Run.Summary.EnabledCount != 1 {
		t.Fatalf("Summary.EnabledCount = %d, want 1", snapshot.Run.Summary.EnabledCount)
	}
}

func TestService_RunPartialDropsResultsForFilesMissingFromCurrentList(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{
			TotalFiles:    2,
			SampledCount:  2,
			KeepCount:     2,
			DisabledCount: 0,
			EnabledCount:  2,
		}},
		Results: []InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: ActionKeep, ActionReason: "no issue detected", Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: ActionKeep, ActionReason: "no issue detected", Disabled: false},
		},
		Settings: DefaultSettings(),
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{
		{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false},
		{FileName: "gamma.json", DisplayName: "Gamma", Disabled: false},
	}}
	prober := &fakeProber{results: []InspectionResultItem{{
		FileName:     "alpha.json",
		DisplayName:  "Alpha",
		Action:       ActionDisable,
		ActionReason: "quota exhausted",
		Disabled:     false,
		Executable:   true,
	}}}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{
		TriggerType: TriggerTypeManual,
		FileNames:   []string{"alpha.json"},
	})
	if err != nil {
		t.Fatalf("Run(partial missing file cleanup) error = %v", err)
	}
	if len(snapshot.Results) != 1 {
		t.Fatalf("len(snapshot.Results) = %d, want 1", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "alpha.json" {
		t.Fatalf("snapshot.Results[0].FileName = %q, want alpha.json", snapshot.Results[0].FileName)
	}
	if snapshot.Results[0].Action != ActionDisable {
		t.Fatalf("snapshot.Results[0].Action = %q, want %q", snapshot.Results[0].Action, ActionDisable)
	}
	if snapshot.Run.Summary.TotalFiles != 2 {
		t.Fatalf("Summary.TotalFiles = %d, want 2", snapshot.Run.Summary.TotalFiles)
	}
	if snapshot.Run.Summary.SampledCount != 1 {
		t.Fatalf("Summary.SampledCount = %d, want 1", snapshot.Run.Summary.SampledCount)
	}
}

func TestService_RunPartialProbeErrorMergesResultsAndRebuildsSummary(t *testing.T) {
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Run: InspectionRunState{Summary: InspectionSummary{
			TotalFiles:    2,
			SampledCount:  2,
			KeepCount:     1,
			DisableCount:  1,
			DisabledCount: 0,
			EnabledCount:  2,
		}},
		Results: []InspectionResultItem{
			{FileName: "alpha.json", DisplayName: "Alpha", Action: ActionKeep, ActionReason: "no issue detected", Disabled: false},
			{FileName: "beta.json", DisplayName: "Beta", Action: ActionDisable, ActionReason: "quota exhausted", Disabled: false},
		},
		Settings: DefaultSettings(),
	}}
	gateway := &fakeGateway{files: []AuthFileRecord{
		{FileName: "alpha.json", DisplayName: "Alpha", Disabled: false},
		{FileName: "beta.json", DisplayName: "Beta", Disabled: false},
	}}
	prober := &fakeProber{
		results: []InspectionResultItem{{
			FileName:     "alpha.json",
			DisplayName:  "Alpha",
			Action:       ActionDelete,
			ActionReason: "401 response",
			Disabled:     false,
		}},
		probeErr: errors.New("probe failed"),
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{
		TriggerType: TriggerTypeManual,
		FileNames:   []string{"alpha.json"},
	})
	if err == nil || err.Error() != "probe failed" {
		t.Fatalf("Run(partial error) error = %v, want probe failed", err)
	}
	if snapshot.Run.Status != RunStatusFailed {
		t.Fatalf("Run(partial error).Run.Status = %q, want %q", snapshot.Run.Status, RunStatusFailed)
	}
	if len(snapshot.Results) != 2 {
		t.Fatalf("len(snapshot.Results) = %d, want 2", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "alpha.json" || snapshot.Results[0].Action != ActionDelete {
		t.Fatalf("snapshot.Results[0] = %+v, want alpha delete", snapshot.Results[0])
	}
	if snapshot.Results[1].FileName != "beta.json" || snapshot.Results[1].Action != ActionDisable {
		t.Fatalf("snapshot.Results[1] = %+v, want beta disable", snapshot.Results[1])
	}
	if snapshot.Run.Summary.TotalFiles != 2 {
		t.Fatalf("Summary.TotalFiles = %d, want 2", snapshot.Run.Summary.TotalFiles)
	}
	if snapshot.Run.Summary.SampledCount != 2 {
		t.Fatalf("Summary.SampledCount = %d, want 2", snapshot.Run.Summary.SampledCount)
	}
	if snapshot.Run.Summary.DeleteCount != 1 {
		t.Fatalf("Summary.DeleteCount = %d, want 1", snapshot.Run.Summary.DeleteCount)
	}
	if snapshot.Run.Summary.DisableCount != 1 {
		t.Fatalf("Summary.DisableCount = %d, want 1", snapshot.Run.Summary.DisableCount)
	}
	if snapshot.Run.Summary.KeepCount != 0 {
		t.Fatalf("Summary.KeepCount = %d, want 0", snapshot.Run.Summary.KeepCount)
	}
}

func TestService_RunScheduledKeepsUnauthorizedResultWhenAutoDeleteFails(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{deleteErrors: map[string]error{"expired.json": errors.New("delete failed")}}
	prober := &fakeProber{
		results: []InspectionResultItem{
			{
				FileName:     "expired.json",
				DisplayName:  "Expired",
				Action:       ActionDelete,
				ActionReason: "401 response",
			},
		},
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err != nil {
		t.Fatalf("Run(scheduled) error = %v", err)
	}
	if len(gateway.deleteCalls) != 1 {
		t.Fatalf("len(deleteCalls) = %d, want 1", len(gateway.deleteCalls))
	}
	if len(snapshot.Results) != 1 {
		t.Fatalf("len(snapshot.Results) = %d, want 1", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "expired.json" {
		t.Fatalf("snapshot.Results[0].FileName = %q, want expired.json", snapshot.Results[0].FileName)
	}
	if snapshot.Run.Summary.AutoDeletedCount != 0 {
		t.Fatalf("AutoDeletedCount = %d, want 0", snapshot.Run.Summary.AutoDeletedCount)
	}
	if snapshot.Run.Summary.DeleteCount != 1 {
		t.Fatalf("DeleteCount = %d, want 1", snapshot.Run.Summary.DeleteCount)
	}
	if snapshot.Run.Summary.ReauthCount != 0 {
		t.Fatalf("ReauthCount = %d, want 0", snapshot.Run.Summary.ReauthCount)
	}
	if len(snapshot.ActionLogs) != 1 {
		t.Fatalf("len(ActionLogs) = %d, want 1", len(snapshot.ActionLogs))
	}
	if snapshot.ActionLogs[0].Success {
		t.Fatalf("ActionLogs[0].Success = true, want false")
	}
	if snapshot.ActionLogs[0].Error != "delete failed" {
		t.Fatalf("ActionLogs[0].Error = %q, want delete failed", snapshot.ActionLogs[0].Error)
	}
}

func TestService_StartRunReturnsQueuedAndCompletesInBackground(t *testing.T) {
	repo := &fakeRepository{snapshot: DefaultSnapshot()}
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "alpha.json", Provider: "codex"}}}
	prober := &blockingProber{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	service := NewService(repo, gateway, prober)

	snapshot, err := service.StartRun(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if snapshot.Run.Status != RunStatusQueued {
		t.Fatalf("StartRun().Run.Status = %q, want %q", snapshot.Run.Status, RunStatusQueued)
	}

	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("background probe did not start")
	}
	close(prober.release)

	deadline := time.Now().Add(time.Second)
	for {
		latest, getErr := service.GetSnapshot(context.Background())
		if getErr != nil {
			t.Fatalf("GetSnapshot() error = %v", getErr)
		}
		if latest.Run.Status == RunStatusCompleted {
			if latest.Run.ProcessedCount != 1 || latest.Run.PendingCount != 0 {
				t.Fatalf("progress = %d processed, %d pending", latest.Run.ProcessedCount, latest.Run.PendingCount)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background run status = %q, want completed", latest.Run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestService_StartRunPersistsFailedStateAfterScheduledContextCancellation(t *testing.T) {
	repo := &contextCheckingRepository{snapshot: DefaultSnapshot()}
	prober := &blockingProber{started: make(chan struct{}, 1), release: make(chan struct{})}
	service := NewService(repo, &fakeGateway{files: []AuthFileRecord{{FileName: "alpha.json", Provider: "codex"}}}, prober)
	ctx, cancel := context.WithCancel(context.Background())
	finishedCh := make(chan LatestSnapshot, 1)

	if _, err := service.StartRunWithCompletion(ctx, RunRequest{TriggerType: TriggerTypeScheduled}, func(snapshot LatestSnapshot) {
		finishedCh <- snapshot
	}); err != nil {
		t.Fatalf("StartRunWithCompletion() error = %v", err)
	}
	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("scheduled probe did not start")
	}
	cancel()

	var finished LatestSnapshot
	select {
	case finished = <-finishedCh:
	case <-time.After(time.Second):
		t.Fatal("scheduled run did not finish after cancellation")
	}
	if finished.Run.Status != RunStatusFailed || finished.Run.Error != context.Canceled.Error() {
		t.Fatalf("finished run = %+v, want failed context canceled", finished.Run)
	}
	persisted, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if persisted.Run.Status != RunStatusFailed {
		t.Fatalf("persisted status = %q, want %q", persisted.Run.Status, RunStatusFailed)
	}
}

func TestService_StartRunRecoversPanicsAndClearsActiveProvider(t *testing.T) {
	service := NewService(
		&fakeRepository{snapshot: DefaultSnapshot()},
		&fakeGateway{files: []AuthFileRecord{{FileName: "alpha.json", Provider: "codex"}}},
		panicProber{},
	)
	finishedCh := make(chan LatestSnapshot, 1)

	if _, err := service.StartRunWithCompletion(context.Background(), RunRequest{TriggerType: TriggerTypeManual}, func(snapshot LatestSnapshot) {
		finishedCh <- snapshot
	}); err != nil {
		t.Fatalf("StartRunWithCompletion() error = %v", err)
	}
	var finished LatestSnapshot
	select {
	case finished = <-finishedCh:
	case <-time.After(time.Second):
		t.Fatal("panicking run did not finish")
	}
	if finished.Run.Status != RunStatusFailed || !strings.Contains(finished.Run.Error, "probe panic") {
		t.Fatalf("finished run = %+v, want failed panic state", finished.Run)
	}

	service.prober = &fakeProber{}
	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual}); err != nil {
		t.Fatalf("Run() after panic error = %v", err)
	}
}

func TestService_RunScheduledProbeErrorDoesNotApplyActions(t *testing.T) {
	gateway := &fakeGateway{files: []AuthFileRecord{{FileName: "expired.json", Provider: "codex"}}}
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, gateway, &fakeProber{
		results:  []InspectionResultItem{{FileName: "expired.json", Provider: "codex", Action: ActionDelete}},
		probeErr: errors.New("probe failed"),
	})

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
	if err == nil || err.Error() != "probe failed" {
		t.Fatalf("Run() error = %v, want probe failed", err)
	}
	if len(gateway.deleteCalls) != 0 {
		t.Fatalf("deleteCalls = %v, want none", gateway.deleteCalls)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Action != ActionDelete {
		t.Fatalf("Results = %+v, want partial delete result", snapshot.Results)
	}
}

func TestService_RunDoesNotApplyEarlierBatchActionsWhenLaterBatchFails(t *testing.T) {
	files := make([]AuthFileRecord, defaultInspectionBatchSize+1)
	for i := range files {
		files[i] = AuthFileRecord{FileName: fmt.Sprintf("auth-%02d.json", i), Provider: "codex"}
	}
	gateway := &fakeGateway{files: files}
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, gateway, &laterBatchErrorProber{})

	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled}); err == nil {
		t.Fatal("Run() error = nil, want later batch failure")
	}
	if len(gateway.deleteCalls) != 0 {
		t.Fatalf("deleteCalls = %v, want none", gateway.deleteCalls)
	}
}

func TestService_RunSampleFiltersResultsForMissingFiles(t *testing.T) {
	settings := DefaultSettings()
	settings.SampleSize = 1
	repo := &fakeRepository{snapshot: LatestSnapshot{
		Settings: settings,
		Results:  []InspectionResultItem{{FileName: "removed.json", Provider: "codex", Action: ActionDelete}},
	}}
	files := []AuthFileRecord{
		{FileName: "alpha.json", Provider: "codex"},
		{FileName: "beta.json", Provider: "codex"},
	}
	service := NewService(repo, &fakeGateway{files: files}, &batchRecordingProber{})

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, result := range snapshot.Results {
		if result.FileName == "removed.json" {
			t.Fatalf("Results = %+v, want removed file filtered", snapshot.Results)
		}
	}
}

func TestService_GetSnapshotDoesNotWaitForAutomaticGatewayAction(t *testing.T) {
	gateway := &fakeGateway{
		files:         []AuthFileRecord{{FileName: "expired.json", Provider: "codex"}},
		deleteStarted: make(chan struct{}, 1),
		deleteRelease: make(chan struct{}),
	}
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, gateway, &fakeProber{
		results: []InspectionResultItem{{FileName: "expired.json", Provider: "codex", Action: ActionDelete}},
	})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeScheduled})
		runDone <- err
	}()

	select {
	case <-gateway.deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic delete did not start")
	}
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := service.GetSnapshot(context.Background())
		snapshotDone <- err
	}()
	select {
	case err := <-snapshotDone:
		if err != nil {
			t.Fatalf("GetSnapshot() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetSnapshot() waited for automatic gateway action")
	}

	close(gateway.deleteRelease)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not finish")
	}
}

func TestService_RunProcessesAccountsInBatches(t *testing.T) {
	files := make([]AuthFileRecord, 53)
	for i := range files {
		files[i] = AuthFileRecord{FileName: fmt.Sprintf("auth-%02d.json", i), Provider: "codex"}
	}
	prober := &batchRecordingProber{}
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, &fakeGateway{files: files}, prober)

	snapshot, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantBatchSizes := []int{25, 25, 3}
	if !slices.Equal(prober.batchSizes, wantBatchSizes) {
		t.Fatalf("batch sizes = %v, want %v", prober.batchSizes, wantBatchSizes)
	}
	if snapshot.Run.BatchSize != defaultInspectionBatchSize {
		t.Fatalf("BatchSize = %d, want %d", snapshot.Run.BatchSize, defaultInspectionBatchSize)
	}
	if snapshot.Run.ProcessedCount != len(files) || snapshot.Run.PendingCount != 0 {
		t.Fatalf("progress = %d processed, %d pending", snapshot.Run.ProcessedCount, snapshot.Run.PendingCount)
	}
}

func TestService_RunRotatesSampleAcrossRuns(t *testing.T) {
	settings := DefaultSettings()
	settings.SampleSize = 2
	files := make([]AuthFileRecord, 6)
	for i := range files {
		files[i] = AuthFileRecord{FileName: fmt.Sprintf("auth-%d.json", i), Provider: "codex"}
	}
	repo := &fakeRepository{snapshot: LatestSnapshot{Settings: settings}}
	prober := &batchRecordingProber{}
	service := NewService(repo, &fakeGateway{files: files}, prober)

	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if got := prober.fileNames[0]; !slices.Equal(got, []string{"auth-0.json", "auth-1.json"}) {
		t.Fatalf("first sample = %v", got)
	}
	prober.fileNames = nil
	prober.batchSizes = nil
	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if got := prober.fileNames[0]; !slices.Equal(got, []string{"auth-2.json", "auth-3.json"}) {
		t.Fatalf("second sample = %v", got)
	}
}

func TestInspectionSettingsForXAILimitsProbePressure(t *testing.T) {
	settings := DefaultSettings()
	settings.Workers = 20
	settings.SampleSize = 100

	adjusted := inspectionSettingsForProvider(settings, "xai")
	if adjusted.Workers != maxXAIInspectionWorkers {
		t.Fatalf("Workers = %d, want %d", adjusted.Workers, maxXAIInspectionWorkers)
	}
	if adjusted.SampleSize != 0 {
		t.Fatalf("SampleSize = %d, want 0 within a prepared batch", adjusted.SampleSize)
	}
	if inspectionBatchSize("xai") != xaiInspectionBatchSize {
		t.Fatalf("xAI batch size = %d, want %d", inspectionBatchSize("xai"), xaiInspectionBatchSize)
	}
}

func TestDefaultProberMapsAuthFilesToKeepResults(t *testing.T) {
	results, err := (DefaultProber{}).ProbeAccounts(context.Background(), []AuthFileRecord{
		{
			FileName:    "codex-alpha.json",
			DisplayName: "Codex Alpha",
			Provider:    "codex",
			AuthIndex:   "7",
			AccountID:   "acct-alpha",
			Disabled:    true,
		},
	}, DefaultSettings())
	if err != nil {
		t.Fatalf("ProbeAccounts() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}

	result := results[0]
	if result.FileName != "codex-alpha.json" {
		t.Fatalf("result.FileName = %q, want %q", result.FileName, "codex-alpha.json")
	}
	if result.DisplayName != "Codex Alpha" {
		t.Fatalf("result.DisplayName = %q, want %q", result.DisplayName, "Codex Alpha")
	}
	if result.Provider != "codex" {
		t.Fatalf("result.Provider = %q, want %q", result.Provider, "codex")
	}
	if result.AuthIndex != "7" {
		t.Fatalf("result.AuthIndex = %q, want %q", result.AuthIndex, "7")
	}
	if result.AccountID != "acct-alpha" {
		t.Fatalf("result.AccountID = %q, want %q", result.AccountID, "acct-alpha")
	}
	if !result.Disabled {
		t.Fatal("result.Disabled = false, want true")
	}
	if result.Action != ActionKeep {
		t.Fatalf("result.Action = %q, want %q", result.Action, ActionKeep)
	}
	if result.ActionReason != "no issue detected" {
		t.Fatalf("result.ActionReason = %q, want %q", result.ActionReason, "no issue detected")
	}
	if result.Executable {
		t.Fatal("result.Executable = true, want false")
	}
}

func TestBuildSummaryCountsResults(t *testing.T) {
	summary := buildSummary([]InspectionResultItem{
		{Action: ActionKeep, Disabled: false},
		{Action: ActionDelete, Disabled: true},
		{Action: ActionDisable, Disabled: true},
		{Action: ActionEnable, Disabled: false},
		{Action: ActionReauth, Disabled: false},
		{Action: ActionFailed, Disabled: false},
		{Action: ActionKeep, Disabled: false},
	}, 7)

	if summary.TotalFiles != 7 {
		t.Fatalf("summary.TotalFiles = %d, want 7", summary.TotalFiles)
	}
	if summary.SampledCount != 7 {
		t.Fatalf("summary.SampledCount = %d, want 7", summary.SampledCount)
	}
	if summary.KeepCount != 2 {
		t.Fatalf("summary.KeepCount = %d, want 2", summary.KeepCount)
	}
	if summary.DeleteCount != 1 {
		t.Fatalf("summary.DeleteCount = %d, want 1", summary.DeleteCount)
	}
	if summary.DisableCount != 1 {
		t.Fatalf("summary.DisableCount = %d, want 1", summary.DisableCount)
	}
	if summary.EnableCount != 1 {
		t.Fatalf("summary.EnableCount = %d, want 1", summary.EnableCount)
	}
	if summary.ReauthCount != 1 {
		t.Fatalf("summary.ReauthCount = %d, want 1", summary.ReauthCount)
	}
	if summary.FailedCount != 1 {
		t.Fatalf("summary.FailedCount = %d, want 1", summary.FailedCount)
	}
	if summary.DisabledCount != 2 {
		t.Fatalf("summary.DisabledCount = %d, want 2", summary.DisabledCount)
	}
	if summary.EnabledCount != 5 {
		t.Fatalf("summary.EnabledCount = %d, want 5", summary.EnabledCount)
	}
}

func TestService_RunClearsActiveAfterSuccess(t *testing.T) {
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, &fakeGateway{}, &fakeProber{})

	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, err := service.Run(context.Background(), RunRequest{TriggerType: TriggerTypeManual}); errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("second Run() error = %v, want not %v", err, ErrRunAlreadyActive)
	} else if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
}

type contextCheckingRepository struct {
	snapshot LatestSnapshot
}

func (r *contextCheckingRepository) Load(ctx context.Context) (LatestSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return LatestSnapshot{}, err
	}
	return r.snapshot, nil
}

func (r *contextCheckingRepository) Save(ctx context.Context, snapshot LatestSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.snapshot = snapshot
	return nil
}

type panicProber struct{}

func (panicProber) ProbeAccounts(context.Context, []AuthFileRecord, InspectionSettings) ([]InspectionResultItem, error) {
	panic("probe panic")
}

type laterBatchErrorProber struct {
	calls int
}

func (p *laterBatchErrorProber) ProbeAccounts(_ context.Context, files []AuthFileRecord, _ InspectionSettings) ([]InspectionResultItem, error) {
	p.calls++
	if p.calls == 1 {
		return []InspectionResultItem{{FileName: files[0].FileName, Provider: "codex", Action: ActionDelete}}, nil
	}
	return keepResults(files), errors.New("later batch failed")
}

type fakeRepository struct {
	snapshot LatestSnapshot
	saved    LatestSnapshot
	saves    []LatestSnapshot
	saveErr  error
}

func (r *fakeRepository) Load(context.Context) (LatestSnapshot, error) {
	return r.snapshot, nil
}

func (r *fakeRepository) Save(_ context.Context, snapshot LatestSnapshot) error {
	r.saved = snapshot
	r.saves = append(r.saves, snapshot)
	r.snapshot = snapshot
	return r.saveErr
}

type setDisabledCall struct {
	name     string
	disabled bool
}

type fakeGateway struct {
	files             []AuthFileRecord
	listErr           error
	receivedProvider  string
	setDisabledCalls  []setDisabledCall
	setDisabledErrors map[string]error
	deleteCalls       [][]string
	deleteErrors      map[string]error
	deleteStarted     chan struct{}
	deleteRelease     chan struct{}
}

func (g *fakeGateway) ListAuthFiles(_ context.Context, provider string) ([]AuthFileRecord, error) {
	g.receivedProvider = provider
	return g.files, g.listErr
}

func (g *fakeGateway) SetDisabled(_ context.Context, name string, disabled bool) error {
	g.setDisabledCalls = append(g.setDisabledCalls, setDisabledCall{name: name, disabled: disabled})
	if g.setDisabledErrors != nil {
		if err := g.setDisabledErrors[name]; err != nil {
			return err
		}
	}
	return nil
}

func (g *fakeGateway) DeleteFiles(ctx context.Context, names []string) error {
	if g.deleteStarted != nil {
		select {
		case g.deleteStarted <- struct{}{}:
		default:
		}
	}
	if g.deleteRelease != nil {
		select {
		case <-g.deleteRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	copied := append([]string(nil), names...)
	g.deleteCalls = append(g.deleteCalls, copied)
	if g.deleteErrors != nil {
		for _, name := range names {
			if err := g.deleteErrors[name]; err != nil {
				return err
			}
		}
	}
	return nil
}

type fakeProber struct {
	results       []InspectionResultItem
	probeErr      error
	receivedFiles []AuthFileRecord
	received      InspectionSettings
}

func (p *fakeProber) ProbeAccounts(_ context.Context, files []AuthFileRecord, settings InspectionSettings) ([]InspectionResultItem, error) {
	p.receivedFiles = append([]AuthFileRecord(nil), files...)
	p.received = settings
	return p.results, p.probeErr
}

type blockingProber struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProber) ProbeAccounts(ctx context.Context, files []AuthFileRecord, _ InspectionSettings) ([]InspectionResultItem, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return keepResults(files), nil
}

type batchRecordingProber struct {
	batchSizes []int
	fileNames  [][]string
}

func (p *batchRecordingProber) ProbeAccounts(_ context.Context, files []AuthFileRecord, _ InspectionSettings) ([]InspectionResultItem, error) {
	p.batchSizes = append(p.batchSizes, len(files))
	names := make([]string, len(files))
	for i := range files {
		names[i] = files[i].FileName
	}
	p.fileNames = append(p.fileNames, names)
	return keepResults(files), nil
}

func keepResults(files []AuthFileRecord) []InspectionResultItem {
	results := make([]InspectionResultItem, len(files))
	for i, file := range files {
		results[i] = InspectionResultItem{
			FileName:     file.FileName,
			DisplayName:  file.DisplayName,
			Provider:     file.Provider,
			Disabled:     file.Disabled,
			Action:       ActionKeep,
			ActionReason: "no issue detected",
		}
	}
	return results
}
