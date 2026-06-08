package codexinspection

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSnapshotRepositorySaveAndLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	repo := NewFileSnapshotRepository(path)

	snapshot := LatestSnapshot{
		Settings: InspectionSettings{
			TargetType:           "server",
			Workers:              8,
			TimeoutSeconds:       25,
			Retries:              2,
			SampleSize:           5,
			UsedPercentThreshold: 90,
			Schedule: InspectionSchedule{
				Enabled:         true,
				Mode:            "interval",
				IntervalMinutes: 30,
			},
		},
		Run: InspectionRunState{
			Status:       RunStatusCompleted,
			TriggerType:  TriggerTypeScheduled,
			StartedAtMS:  1717831200000,
			FinishedAtMS: 1717831500000,
			Summary: InspectionSummary{
				TotalFiles:       12,
				SampledCount:     5,
				KeepCount:        2,
				DeleteCount:      1,
				DisableCount:     1,
				EnableCount:      0,
				ReauthCount:      1,
				DisabledCount:    4,
				EnabledCount:     8,
				AutoDeletedCount: 2,
			},
		},
		Results: []InspectionResultItem{
			{
				FileName:     "auths/codex-01.json",
				DisplayName:  "codex-01",
				Provider:     "codex",
				AuthIndex:    "01",
				AccountID:    "acc-1",
				Disabled:     false,
				StatusCode:   401,
				UsedPercent:  intPtr(93),
				Error:        "reauth required",
				Action:       ActionReauth,
				ActionReason: "token expired",
				Executable:   true,
			},
		},
		ActionLogs: []InspectionActionLog{
			{
				Action:       ActionReauth,
				FileName:     "auths/codex-01.json",
				DisplayName:  "codex-01",
				Success:      true,
				ExecutedAtMS: 1717831560000,
			},
		},
	}

	if err := repo.Save(ctx, snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Settings.TargetType != "server" {
		t.Fatalf("Load().Settings.TargetType = %q, want %q", loaded.Settings.TargetType, "server")
	}
	if loaded.Settings.TimeoutSeconds != 25 {
		t.Fatalf("Load().Settings.TimeoutSeconds = %d, want 25", loaded.Settings.TimeoutSeconds)
	}
	if loaded.Settings.Schedule.Mode != "interval" {
		t.Fatalf("Load().Settings.Schedule.Mode = %q, want %q", loaded.Settings.Schedule.Mode, "interval")
	}
	if loaded.Run.TriggerType != TriggerTypeScheduled {
		t.Fatalf("Load().Run.TriggerType = %q, want %q", loaded.Run.TriggerType, TriggerTypeScheduled)
	}
	if loaded.Run.StartedAtMS != 1717831200000 {
		t.Fatalf("Load().Run.StartedAtMS = %d, want %d", loaded.Run.StartedAtMS, int64(1717831200000))
	}
	if loaded.Run.Summary.ReauthCount != 1 {
		t.Fatalf("Load().Run.Summary.ReauthCount = %d, want 1", loaded.Run.Summary.ReauthCount)
	}
	if loaded.Run.Summary.AutoDeletedCount != 2 {
		t.Fatalf("Load().Run.Summary.AutoDeletedCount = %d, want 2", loaded.Run.Summary.AutoDeletedCount)
	}
	if len(loaded.Results) != 1 {
		t.Fatalf("len(Load().Results) = %d, want 1", len(loaded.Results))
	}
	if loaded.Results[0].FileName != "auths/codex-01.json" {
		t.Fatalf("Load().Results[0].FileName = %q, want %q", loaded.Results[0].FileName, "auths/codex-01.json")
	}
	if loaded.Results[0].Action != ActionReauth {
		t.Fatalf("Load().Results[0].Action = %q, want %q", loaded.Results[0].Action, ActionReauth)
	}
	if loaded.Results[0].UsedPercent == nil || *loaded.Results[0].UsedPercent != 93 {
		t.Fatalf("Load().Results[0].UsedPercent = %v, want 93", loaded.Results[0].UsedPercent)
	}
	if len(loaded.ActionLogs) != 1 {
		t.Fatalf("len(Load().ActionLogs) = %d, want 1", len(loaded.ActionLogs))
	}
	if !loaded.ActionLogs[0].Success {
		t.Fatal("Load().ActionLogs[0].Success = false, want true")
	}
}

func intPtr(value int) *int {
	return &value
}

func TestFileSnapshotRepositoryLoadMissingFileReturnsDefaultSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	repo := NewFileSnapshotRepository(path)

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := DefaultSnapshot()
	if loaded.Settings.TargetType != want.Settings.TargetType {
		t.Fatalf("Load().Settings.TargetType = %q, want %q", loaded.Settings.TargetType, want.Settings.TargetType)
	}
	if loaded.Run.Status != want.Run.Status {
		t.Fatalf("Load().Run.Status = %q, want %q", loaded.Run.Status, want.Run.Status)
	}
	if len(loaded.Results) != 0 {
		t.Fatalf("len(Load().Results) = %d, want 0", len(loaded.Results))
	}
	if len(loaded.ActionLogs) != 0 {
		t.Fatalf("len(Load().ActionLogs) = %d, want 0", len(loaded.ActionLogs))
	}
}

func TestFileSnapshotRepositoryLoadBackfillsDefaultSettingsWhenTargetTypeMissing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	repo := NewFileSnapshotRepository(path)

	broken := `{
  "settings": {
    "workers": 11,
    "targetType": ""
  },
  "run": {
    "status": "running",
    "triggerType": "manual"
  },
  "results": null,
  "actionLogs": null
}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	defaults := DefaultSettings()
	if loaded.Settings.TargetType != defaults.TargetType {
		t.Fatalf("Load().Settings.TargetType = %q, want %q", loaded.Settings.TargetType, defaults.TargetType)
	}
	if loaded.Settings.Workers != defaults.Workers {
		t.Fatalf("Load().Settings.Workers = %d, want %d", loaded.Settings.Workers, defaults.Workers)
	}
	if loaded.Settings.TimeoutSeconds != defaults.TimeoutSeconds {
		t.Fatalf("Load().Settings.TimeoutSeconds = %d, want %d", loaded.Settings.TimeoutSeconds, defaults.TimeoutSeconds)
	}
	if loaded.Settings.Schedule.Mode != defaults.Schedule.Mode {
		t.Fatalf("Load().Settings.Schedule.Mode = %q, want %q", loaded.Settings.Schedule.Mode, defaults.Schedule.Mode)
	}
	if loaded.Run.Status != RunStatusRunning {
		t.Fatalf("Load().Run.Status = %q, want %q", loaded.Run.Status, RunStatusRunning)
	}
	if loaded.Run.TriggerType != TriggerTypeManual {
		t.Fatalf("Load().Run.TriggerType = %q, want %q", loaded.Run.TriggerType, TriggerTypeManual)
	}
	if loaded.Results == nil {
		t.Fatal("Load().Results = nil, want empty slice")
	}
	if loaded.ActionLogs == nil {
		t.Fatal("Load().ActionLogs = nil, want empty slice")
	}
}

func TestFileSnapshotRepositorySaveWritesIndentedJSON(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	repo := NewFileSnapshotRepository(path)

	if err := repo.Save(ctx, DefaultSnapshot()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	content := string(data)

	for _, want := range []string{"{\n", "  \"settings\":", "  \"run\":"} {
		if !strings.Contains(content, want) {
			t.Fatalf("saved JSON missing %q in:\n%s", want, content)
		}
	}
}

func TestFileSnapshotRepositoryLoadRestoresFromExternalStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	snapshot := DefaultSnapshot()
	snapshot.Settings.Workers = 9
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	data = append(data, '\n')

	external := &stubSnapshotExternalStore{loadData: data, loadOK: true}
	repo := NewFileSnapshotRepository(path, external)

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Settings.Workers != 9 {
		t.Fatalf("Load().Settings.Workers = %d, want 9", loaded.Settings.Workers)
	}
	localData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(localData) != string(data) {
		t.Fatalf("local snapshot mismatch after restore")
	}
}

func TestFileSnapshotRepositorySavePersistsToExternalStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "latest-snapshot.json")
	external := &stubSnapshotExternalStore{}
	repo := NewFileSnapshotRepository(path, external)

	snapshot := DefaultSnapshot()
	snapshot.Settings.Workers = 12
	if err := repo.Save(ctx, snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if external.saveCalls != 1 {
		t.Fatalf("saveCalls = %d, want 1", external.saveCalls)
	}
	if len(external.savedData) == 0 {
		t.Fatal("savedData is empty")
	}
}

func TestFileSnapshotRepositorySaveDoesNotLeaveTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "latest-snapshot.json")
	repo := NewFileSnapshotRepository(path)

	if err := repo.Save(ctx, DefaultSnapshot()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("unexpected temp file left behind: %s", entry.Name())
		}
	}
}

type stubSnapshotExternalStore struct {
	loadData  []byte
	loadOK    bool
	savedData []byte
	saveCalls int
}

func (s *stubSnapshotExternalStore) CodexInspectionSnapshotPath() string {
	return ""
}

func (s *stubSnapshotExternalStore) LoadCodexInspectionSnapshot(context.Context) ([]byte, bool, error) {
	if !s.loadOK {
		return nil, false, nil
	}
	return append([]byte(nil), s.loadData...), true, nil
}

func (s *stubSnapshotExternalStore) SaveCodexInspectionSnapshot(_ context.Context, data []byte) error {
	s.saveCalls++
	s.savedData = append([]byte(nil), data...)
	return nil
}
