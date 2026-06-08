package codexinspection

import (
	"context"
	"errors"
	"time"
)

var ErrRunAlreadyActive = errors.New("codex inspection run is already active")
var ErrDeleteConfirmationRequired = errors.New("codex inspection delete confirmation is required")

type AuthFileRecord struct {
	FileName    string
	DisplayName string
	Provider    string
	AuthIndex   string
	AccountID   string
	Disabled    bool
}

type StatusPatch struct {
	Name     string
	Disabled bool
}

type AuthFileGateway interface {
	ListCodexAuthFiles(ctx context.Context) ([]AuthFileRecord, error)
	SetDisabled(ctx context.Context, name string, disabled bool) error
	DeleteFiles(ctx context.Context, names []string) error
}

type Prober interface {
	ProbeCodexAccounts(ctx context.Context, files []AuthFileRecord, settings InspectionSettings) ([]InspectionResultItem, error)
}

type RunRequest struct {
	TriggerType TriggerType
}

type ExecuteActionsRequest struct {
	Action        Action
	FileNames     []string
	ConfirmDelete bool
}

type ExecuteActionsResult struct {
	Snapshot LatestSnapshot        `json:"snapshot"`
	Logs     []InspectionActionLog `json:"logs"`
}

type Service struct {
	repo    SnapshotRepository
	gateway AuthFileGateway
	prober  Prober
	mu      chan struct{}
	active  bool
}

func NewService(repo SnapshotRepository, gateway AuthFileGateway, prober Prober) *Service {
	return &Service{
		repo:    repo,
		gateway: gateway,
		prober:  prober,
		mu:      make(chan struct{}, 1),
	}
}

func (s *Service) Run(ctx context.Context, req RunRequest) (snapshot LatestSnapshot, err error) {
	s.lock()
	if s.active {
		s.unlock()
		return LatestSnapshot{}, ErrRunAlreadyActive
	}
	s.active = true
	s.unlock()

	defer func() {
		s.lock()
		s.active = false
		s.unlock()
	}()

	snapshot, err = s.repo.Load(ctx)
	if err != nil {
		return LatestSnapshot{}, err
	}

	startedAtMS := nowMillis()
	nextTriggerAtMS := int64(0)
	if snapshot.Settings.Schedule.Enabled && snapshot.Settings.Schedule.IntervalMinutes > 0 {
		nextTriggerAtMS = snapshot.Run.NextTriggerAtMS
	}
	snapshot.Run = InspectionRunState{
		Status:          RunStatusRunning,
		TriggerType:     req.TriggerType,
		StartedAtMS:     startedAtMS,
		NextTriggerAtMS: nextTriggerAtMS,
	}
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return LatestSnapshot{}, err
	}

	files, err := s.gateway.ListCodexAuthFiles(ctx)
	if err != nil {
		return LatestSnapshot{}, err
	}

	results, probeErr := s.prober.ProbeCodexAccounts(ctx, files, snapshot.Settings)
	if probeErr != nil {
		snapshot.Results = results
		snapshot.Run.Status = RunStatusFailed
		snapshot.Run.FinishedAtMS = nowMillis()
		snapshot.Run.Error = probeErr.Error()
		snapshot.Run.Summary = buildSummary(results, len(files))
		if saveErr := s.repo.Save(ctx, snapshot); saveErr != nil {
			return LatestSnapshot{}, saveErr
		}
		return snapshot, probeErr
	}

	autoDeleteLogs := []InspectionActionLog{}
	autoDeletedCount := 0
	if req.TriggerType == TriggerTypeScheduled {
		results, autoDeleteLogs, autoDeletedCount = s.autoDeleteUnauthorizedResults(ctx, results)
	}

	snapshot.Results = results
	snapshot.ActionLogs = autoDeleteLogs
	snapshot.Run.Status = RunStatusCompleted
	snapshot.Run.FinishedAtMS = nowMillis()
	snapshot.Run.Error = ""
	snapshot.Run.Summary = buildSummary(results, len(files))
	snapshot.Run.Summary.AutoDeletedCount = autoDeletedCount
	if snapshot.Settings.Schedule.Enabled && snapshot.Settings.Schedule.IntervalMinutes > 0 {
		if req.TriggerType == TriggerTypeScheduled {
			snapshot.Run.NextTriggerAtMS = nowMillis() + int64(time.Duration(snapshot.Settings.Schedule.IntervalMinutes)*time.Minute/time.Millisecond)
		}
	} else {
		snapshot.Run.NextTriggerAtMS = 0
	}
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return LatestSnapshot{}, err
	}

	return snapshot, nil
}

func (s *Service) ExecuteActions(ctx context.Context, req ExecuteActionsRequest) (ExecuteActionsResult, error) {
	if req.Action == ActionDelete && !req.ConfirmDelete {
		return ExecuteActionsResult{}, ErrDeleteConfirmationRequired
	}

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return ExecuteActionsResult{}, err
	}

	displayNames := map[string]string{}
	for _, item := range snapshot.Results {
		displayNames[item.FileName] = item.DisplayName
	}

	logs := make([]InspectionActionLog, 0, len(req.FileNames))
	for _, fileName := range req.FileNames {
		log := InspectionActionLog{
			Action:       req.Action,
			FileName:     fileName,
			DisplayName:  displayNames[fileName],
			Success:      true,
			ExecutedAtMS: nowMillis(),
		}

		var callErr error
		switch req.Action {
		case ActionDisable:
			callErr = s.gateway.SetDisabled(ctx, fileName, true)
		case ActionEnable:
			callErr = s.gateway.SetDisabled(ctx, fileName, false)
		case ActionDelete:
			callErr = s.gateway.DeleteFiles(ctx, []string{fileName})
		case ActionKeep, ActionReauth:
		}

		if callErr != nil {
			log.Success = false
			log.Error = callErr.Error()
		} else {
			switch req.Action {
			case ActionDisable:
				updateResultDisabled(snapshot.Results, fileName, true)
			case ActionEnable:
				updateResultDisabled(snapshot.Results, fileName, false)
			case ActionDelete:
				snapshot.Results = deleteResultByFileName(snapshot.Results, fileName)
			}
		}
		logs = append(logs, log)
	}

	snapshot.ActionLogs = logs
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return ExecuteActionsResult{}, err
	}

	return ExecuteActionsResult{Snapshot: snapshot, Logs: logs}, nil
}

func (s *Service) autoDeleteUnauthorizedResults(ctx context.Context, results []InspectionResultItem) ([]InspectionResultItem, []InspectionActionLog, int) {
	nextResults := make([]InspectionResultItem, 0, len(results))
	logs := make([]InspectionActionLog, 0)
	autoDeletedCount := 0

	for _, result := range results {
		if result.Action != ActionDelete || result.ActionReason != "401 response" {
			nextResults = append(nextResults, result)
			continue
		}

		log := InspectionActionLog{
			Action:       ActionDelete,
			FileName:     result.FileName,
			DisplayName:  result.DisplayName,
			Success:      true,
			ExecutedAtMS: nowMillis(),
		}
		if err := s.gateway.DeleteFiles(ctx, []string{result.FileName}); err != nil {
			log.Success = false
			log.Error = err.Error()
			nextResults = append(nextResults, result)
		} else {
			autoDeletedCount++
		}
		logs = append(logs, log)
	}

	return nextResults, logs, autoDeletedCount
}

func updateResultDisabled(results []InspectionResultItem, fileName string, disabled bool) {
	for i := range results {
		if results[i].FileName == fileName {
			results[i].Disabled = disabled
			return
		}
	}
}

func deleteResultByFileName(results []InspectionResultItem, fileName string) []InspectionResultItem {
	filtered := results[:0]
	for _, result := range results {
		if result.FileName != fileName {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func (s *Service) lock() {
	s.mu <- struct{}{}
}

func (s *Service) unlock() {
	<-s.mu
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
