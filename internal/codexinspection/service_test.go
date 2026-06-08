package codexinspection

import (
	"context"
	"errors"
	"testing"
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

func TestService_RunRejectsConcurrentRun(t *testing.T) {
	service := NewService(&fakeRepository{snapshot: DefaultSnapshot()}, &fakeGateway{}, &fakeProber{})
	service.active = true

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

func TestService_RunScheduledAutoDeletesUnauthorizedResults(t *testing.T) {
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
				FileName:     "healthy.json",
				DisplayName:  "Healthy",
				Action:       ActionKeep,
				ActionReason: "no issue detected",
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
	if len(gateway.deleteCalls[0]) != 1 || gateway.deleteCalls[0][0] != "expired.json" {
		t.Fatalf("delete call = %#v, want expired.json", gateway.deleteCalls[0])
	}
	if len(snapshot.Results) != 1 {
		t.Fatalf("len(snapshot.Results) = %d, want 1", len(snapshot.Results))
	}
	if snapshot.Results[0].FileName != "healthy.json" {
		t.Fatalf("snapshot.Results[0].FileName = %q, want healthy.json", snapshot.Results[0].FileName)
	}
	if snapshot.Run.Summary.AutoDeletedCount != 1 {
		t.Fatalf("AutoDeletedCount = %d, want 1", snapshot.Run.Summary.AutoDeletedCount)
	}
	if snapshot.Run.Summary.ReauthCount != 0 {
		t.Fatalf("ReauthCount = %d, want 0", snapshot.Run.Summary.ReauthCount)
	}
	if len(snapshot.ActionLogs) != 1 {
		t.Fatalf("len(ActionLogs) = %d, want 1", len(snapshot.ActionLogs))
	}
	if snapshot.ActionLogs[0].Action != ActionDelete || !snapshot.ActionLogs[0].Success {
		t.Fatalf("ActionLogs[0] = %+v, want successful delete", snapshot.ActionLogs[0])
	}
}

func TestService_RunManualDoesNotAutoDeleteUnauthorizedResults(t *testing.T) {
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
	if len(snapshot.Results) != 1 {
		t.Fatalf("len(snapshot.Results) = %d, want 1", len(snapshot.Results))
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

func TestDefaultProberMapsAuthFilesToKeepResults(t *testing.T) {
	results, err := (DefaultProber{}).ProbeCodexAccounts(context.Background(), []AuthFileRecord{
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
		t.Fatalf("ProbeCodexAccounts() error = %v", err)
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
		{Action: ActionKeep, Disabled: false},
	}, 6)

	if summary.TotalFiles != 6 {
		t.Fatalf("summary.TotalFiles = %d, want 6", summary.TotalFiles)
	}
	if summary.SampledCount != 6 {
		t.Fatalf("summary.SampledCount = %d, want 6", summary.SampledCount)
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
	if summary.DisabledCount != 2 {
		t.Fatalf("summary.DisabledCount = %d, want 2", summary.DisabledCount)
	}
	if summary.EnabledCount != 4 {
		t.Fatalf("summary.EnabledCount = %d, want 4", summary.EnabledCount)
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
	setDisabledCalls  []setDisabledCall
	setDisabledErrors map[string]error
	deleteCalls       [][]string
	deleteErrors      map[string]error
}

func (g *fakeGateway) ListCodexAuthFiles(context.Context) ([]AuthFileRecord, error) {
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

func (g *fakeGateway) DeleteFiles(_ context.Context, names []string) error {
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

func (p *fakeProber) ProbeCodexAccounts(_ context.Context, files []AuthFileRecord, settings InspectionSettings) ([]InspectionResultItem, error) {
	p.receivedFiles = append([]AuthFileRecord(nil), files...)
	p.received = settings
	return p.results, p.probeErr
}
