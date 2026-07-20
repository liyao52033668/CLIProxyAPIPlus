package codexinspection

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var ErrRunAlreadyActive = errors.New("codex inspection run is already active")
var ErrDeleteConfirmationRequired = errors.New("codex inspection delete confirmation is required")

type AuthFileRecord struct {
	AuthID      string
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
	ListAuthFiles(ctx context.Context, provider string) ([]AuthFileRecord, error)
	SetDisabled(ctx context.Context, name string, disabled bool) error
	DeleteFiles(ctx context.Context, names []string) error
}

type Prober interface {
	ProbeAccounts(ctx context.Context, files []AuthFileRecord, settings InspectionSettings) ([]InspectionResultItem, error)
}

type RunRequest struct {
	TriggerType TriggerType
	Provider    string   `json:"provider,omitempty"`
	FileNames   []string `json:"fileNames,omitempty"`
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

const (
	defaultInspectionBatchSize = 25
	xaiInspectionBatchSize     = 5
	maxInspectionWorkers       = 8
	maxProviderWorkers         = 4
	maxXAIInspectionWorkers    = 2
	maxConcurrentProviderRuns  = 2
	xaiInspectionBatchPause    = 500 * time.Millisecond
)

type Service struct {
	repo            SnapshotRepository
	gateway         AuthFileGateway
	prober          Prober
	mu              sync.Mutex
	actionsMu       sync.Mutex
	activeProviders map[string]struct{}
	runSlots        chan struct{}
}

type inspectionRunPlan struct {
	provider string
	request  RunRequest
	settings InspectionSettings
}

func NewService(repo SnapshotRepository, gateway AuthFileGateway, prober Prober) *Service {
	return &Service{
		repo:            repo,
		gateway:         gateway,
		prober:          prober,
		activeProviders: make(map[string]struct{}),
		runSlots:        make(chan struct{}, maxConcurrentProviderRuns),
	}
}

func (s *Service) GetSnapshot(ctx context.Context) (LatestSnapshot, error) {
	s.lock()
	snapshot, err := s.repo.Load(ctx)
	s.unlock()
	if err != nil {
		return LatestSnapshot{}, err
	}
	if s.gateway == nil {
		return snapshot, nil
	}

	provider := normalizeProvider(snapshot.Settings.TargetType)
	if provider == "" {
		provider = DefaultSettings().TargetType
	}
	files, err := s.gateway.ListAuthFiles(ctx, provider)
	if err != nil {
		return LatestSnapshot{}, err
	}

	s.lock()
	defer s.unlock()
	latest, err := s.repo.Load(ctx)
	if err != nil {
		return LatestSnapshot{}, err
	}
	latestProvider := normalizeProvider(latest.Settings.TargetType)
	if latestProvider == "" {
		latestProvider = DefaultSettings().TargetType
	}
	if latestProvider != provider {
		return latest, nil
	}
	return s.reconcileSnapshot(ctx, latest, provider, files)
}

func (s *Service) Run(ctx context.Context, req RunRequest) (LatestSnapshot, error) {
	_, plan, err := s.beginRun(ctx, req, RunStatusRunning)
	if err != nil {
		return LatestSnapshot{}, err
	}
	return s.executeRun(ctx, plan)
}

func (s *Service) StartRun(ctx context.Context, req RunRequest) (LatestSnapshot, error) {
	return s.startRun(ctx, req, nil)
}

func (s *Service) StartRunWithCompletion(ctx context.Context, req RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error) {
	return s.startRun(ctx, req, onFinish)
}

func (s *Service) startRun(ctx context.Context, req RunRequest, onFinish func(LatestSnapshot)) (LatestSnapshot, error) {
	snapshot, plan, err := s.beginRun(ctx, req, RunStatusQueued)
	if err != nil {
		return LatestSnapshot{}, err
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	} else if req.TriggerType == TriggerTypeManual {
		runCtx = context.WithoutCancel(runCtx)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicErr := fmt.Errorf("codex inspection panic: %v", recovered)
				log.WithField("provider", plan.provider).WithError(panicErr).Error("codex inspection run panicked")
				snapshot, _ := s.finishRun(context.WithoutCancel(runCtx), plan, RunStatusFailed, panicErr)
				if onFinish != nil {
					onFinish(snapshot)
				}
			}
		}()
		finished, _ := s.executeRun(runCtx, plan)
		if onFinish != nil {
			onFinish(finished)
		}
	}()
	return snapshot, nil
}

func (s *Service) beginRun(ctx context.Context, req RunRequest, status RunStatus) (LatestSnapshot, inspectionRunPlan, error) {
	s.lock()
	defer s.unlock()

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return LatestSnapshot{}, inspectionRunPlan{}, err
	}
	provider := normalizeProvider(req.Provider)
	if provider == "" {
		provider = normalizeProvider(snapshot.Settings.TargetType)
	}
	if provider == "" {
		provider = DefaultSettings().TargetType
	}
	if _, exists := s.activeProviders[provider]; exists {
		return LatestSnapshot{}, inspectionRunPlan{}, ErrRunAlreadyActive
	}

	if req.TriggerType == TriggerTypeManual {
		selectProviderView(&snapshot, provider)
	} else {
		initializeProviderStates(&snapshot)
	}
	state := providerState(snapshot, provider)
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	if nextTriggers == nil {
		nextTriggers = make(map[string]int64)
	}
	schedule := snapshot.Settings.ScheduleFor(provider)
	if !schedule.Enabled || schedule.IntervalMinutes <= 0 {
		delete(nextTriggers, provider)
	} else if req.TriggerType == TriggerTypeScheduled {
		nextTriggers[provider] = nowMillis() + intervalMilliseconds(schedule.IntervalMinutes)
	}
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
	state.Run = InspectionRunState{
		Status:         status,
		TriggerType:    req.TriggerType,
		StartedAtMS:    nowMillis(),
		BatchSize:      inspectionBatchSize(provider),
		ProcessedCount: 0,
		PendingCount:   0,
	}
	setProviderState(&snapshot, provider, state)
	s.activeProviders[provider] = struct{}{}
	if err = s.repo.Save(ctx, snapshot); err != nil {
		delete(s.activeProviders, provider)
		return LatestSnapshot{}, inspectionRunPlan{}, err
	}

	plan := inspectionRunPlan{provider: provider, request: req, settings: snapshot.Settings}
	return snapshotForProvider(snapshot, provider), plan, nil
}

func (s *Service) executeRun(ctx context.Context, plan inspectionRunPlan) (LatestSnapshot, error) {
	select {
	case s.runSlots <- struct{}{}:
		defer func() { <-s.runSlots }()
	case <-ctx.Done():
		return s.finishRun(ctx, plan, RunStatusFailed, ctx.Err())
	}

	files, err := s.gateway.ListAuthFiles(ctx, plan.provider)
	if err != nil {
		return s.finishRun(ctx, plan, RunStatusFailed, err)
	}
	probeFiles, err := s.initializeRun(ctx, plan, files)
	if err != nil {
		return s.finishRun(ctx, plan, RunStatusFailed, err)
	}

	batchSize := inspectionBatchSize(plan.provider)
	batchSettings := inspectionSettingsForProvider(plan.settings, plan.provider)
	runResults := make([]InspectionResultItem, 0, len(probeFiles))
	for start := 0; start < len(probeFiles) || start == 0; start += batchSize {
		end := min(start+batchSize, len(probeFiles))
		batch := probeFiles[start:end]
		results, probeErr := s.prober.ProbeAccounts(ctx, batch, batchSettings)
		results = applyXAIRecoveryActions(results)
		runResults = append(runResults, results...)
		if commitErr := s.commitBatch(ctx, plan, files, batch, results); commitErr != nil {
			return s.finishRun(ctx, plan, RunStatusFailed, commitErr)
		}
		if probeErr != nil {
			return s.finishRun(ctx, plan, RunStatusFailed, probeErr)
		}
		if plan.provider == "xai" && end < len(probeFiles) {
			if waitErr := waitInspectionBatch(ctx, xaiInspectionBatchPause); waitErr != nil {
				return s.finishRun(ctx, plan, RunStatusFailed, waitErr)
			}
		}
	}
	if actionErr := s.applyRunActions(ctx, plan, files, runResults); actionErr != nil {
		return s.finishRun(ctx, plan, RunStatusFailed, actionErr)
	}
	return s.finishRun(ctx, plan, RunStatusCompleted, nil)
}

func (s *Service) initializeRun(ctx context.Context, plan inspectionRunPlan, files []AuthFileRecord) ([]AuthFileRecord, error) {
	s.lock()
	defer s.unlock()

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	state := providerState(snapshot, plan.provider)
	reconcileAutoDisabledFiles(&snapshot, plan.provider, files)
	probeFiles := filterAuthFilesByName(files, plan.request.FileNames)
	if len(plan.request.FileNames) == 0 {
		probeFiles = rotateAuthFiles(probeFiles, state.Cursor)
		if plan.settings.SampleSize > 0 && plan.settings.SampleSize < len(probeFiles) {
			probeFiles = probeFiles[:plan.settings.SampleSize]
			state.Results = filterResultsByCurrentFiles(state.Results, files)
		} else {
			state.Results = []InspectionResultItem{}
		}
	} else {
		state.Results = filterResultsByCurrentFiles(state.Results, files)
	}
	state.ActionLogs = []InspectionActionLog{}
	state.Run.Status = RunStatusRunning
	state.Run.BatchSize = inspectionBatchSize(plan.provider)
	state.Run.ProcessedCount = 0
	state.Run.PendingCount = len(probeFiles)
	state.Run.Summary = buildSummary(state.Results, len(files))
	setProviderState(&snapshot, plan.provider, state)
	if err = s.repo.Save(ctx, snapshot); err != nil {
		return nil, err
	}
	return probeFiles, nil
}

func (s *Service) commitBatch(ctx context.Context, plan inspectionRunPlan, files, batch []AuthFileRecord, results []InspectionResultItem) error {
	s.lock()
	defer s.unlock()

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	state := providerState(snapshot, plan.provider)
	state.Results = mergeRunResults(state.Results, results)
	state.Run.ProcessedCount += len(batch)
	state.Run.PendingCount = max(0, state.Run.PendingCount-len(batch))
	previousAutoDeleted := state.Run.Summary.AutoDeletedCount
	state.Run.Summary = buildSummary(state.Results, len(files))
	state.Run.Summary.AutoDeletedCount = previousAutoDeleted
	if len(plan.request.FileNames) == 0 && len(files) > 0 {
		state.Cursor = (state.Cursor + len(batch)) % len(files)
	}
	setProviderState(&snapshot, plan.provider, state)
	return s.repo.Save(ctx, snapshot)
}

func (s *Service) applyRunActions(ctx context.Context, plan inspectionRunPlan, files []AuthFileRecord, results []InspectionResultItem) error {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()

	s.lock()
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		s.unlock()
		return err
	}
	results = append([]InspectionResultItem(nil), results...)
	snapshot.AutoDisabledFiles = cloneAutoDisabledFiles(snapshot.AutoDisabledFiles)
	touchedProviders := map[string]struct{}{plan.provider: {}}
	for _, result := range results {
		resultProvider := normalizeProvider(result.Provider)
		if resultProvider != "" {
			touchedProviders[resultProvider] = struct{}{}
		}
	}
	s.unlock()

	autoActionLogs := []InspectionActionLog{}
	autoDeletedCount := 0
	if plan.request.TriggerType == TriggerTypeScheduled {
		results, autoActionLogs, autoDeletedCount = s.autoApplyScheduledActions(ctx, plan.provider, results, &snapshot)
	} else {
		results, autoActionLogs = s.autoApplyXAIRecoveryActions(ctx, plan.provider, results, &snapshot)
	}

	s.lock()
	defer s.unlock()

	latest, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	state := providerState(latest, plan.provider)
	for _, actionLog := range autoActionLogs {
		if actionLog.Success && actionLog.Action == ActionDelete {
			state.Results = deleteResultByFileName(state.Results, actionLog.FileName)
		}
	}
	state.Results = mergeRunResults(state.Results, results)
	state.ActionLogs = append(state.ActionLogs, autoActionLogs...)
	previousAutoDeleted := state.Run.Summary.AutoDeletedCount
	state.Run.Summary = buildSummary(state.Results, len(files))
	state.Run.Summary.AutoDeletedCount = previousAutoDeleted + autoDeletedCount
	for touchedProvider := range touchedProviders {
		replaceAutoDisabledFiles(&latest, touchedProvider, snapshot.AutoDisabledFiles[touchedProvider])
	}
	setProviderState(&latest, plan.provider, state)
	return s.repo.Save(ctx, latest)
}

func (s *Service) finishRun(ctx context.Context, plan inspectionRunPlan, status RunStatus, runErr error) (LatestSnapshot, error) {
	s.lock()
	defer s.unlock()
	defer delete(s.activeProviders, plan.provider)

	persistCtx := context.Background()
	if ctx != nil {
		persistCtx = context.WithoutCancel(ctx)
	}
	snapshot, err := s.repo.Load(persistCtx)
	if err != nil {
		if runErr != nil {
			return LatestSnapshot{}, runErr
		}
		return LatestSnapshot{}, err
	}
	state := providerState(snapshot, plan.provider)
	state.Run.Status = status
	state.Run.FinishedAtMS = nowMillis()
	if status == RunStatusCompleted {
		state.Run.PendingCount = 0
		state.Run.Error = ""
	} else if runErr != nil {
		state.Run.Error = runErr.Error()
	}
	setProviderState(&snapshot, plan.provider, state)
	updateProviderNextTrigger(&snapshot, plan.provider, plan.request.TriggerType)
	if saveErr := s.repo.Save(persistCtx, snapshot); saveErr != nil {
		return LatestSnapshot{}, saveErr
	}
	return snapshotForProvider(snapshot, plan.provider), runErr
}

func (s *Service) UpdateSettings(ctx context.Context, settings InspectionSettings) (LatestSnapshot, error) {
	s.lock()
	defer s.unlock()

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return LatestSnapshot{}, err
	}
	settings.TargetType = normalizeProvider(settings.TargetType)
	if settings.TargetType == "" {
		settings.TargetType = DefaultSettings().TargetType
	}
	mergedSchedules := maps.Clone(snapshot.Settings.Schedules)
	if mergedSchedules == nil {
		mergedSchedules = make(map[string]InspectionSchedule)
	}
	for provider, schedule := range settings.Schedules {
		provider = normalizeProvider(provider)
		if provider != "" {
			mergedSchedules[provider] = schedule
		}
	}
	settings.Schedules = mergedSchedules

	previousSchedule := snapshot.Settings.ScheduleFor(settings.TargetType)
	selectProviderView(&snapshot, settings.TargetType)
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	if nextTriggers == nil {
		nextTriggers = make(map[string]int64)
	}
	nextSchedule := settings.ScheduleFor(settings.TargetType)
	if nextSchedule.Enabled && nextSchedule.IntervalMinutes > 0 {
		if previousSchedule != nextSchedule || nextTriggers[settings.TargetType] <= 0 {
			nextTriggers[settings.TargetType] = nowMillis() + intervalMilliseconds(nextSchedule.IntervalMinutes)
		}
	} else {
		delete(nextTriggers, settings.TargetType)
	}
	snapshot.Settings = settings
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
	if err = s.repo.Save(ctx, snapshot); err != nil {
		return LatestSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) ExecuteActions(ctx context.Context, req ExecuteActionsRequest) (ExecuteActionsResult, error) {
	if req.Action == ActionDelete && !req.ConfirmDelete {
		return ExecuteActionsResult{}, ErrDeleteConfirmationRequired
	}

	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()

	s.lock()
	snapshot, err := s.repo.Load(ctx)
	s.unlock()
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	initializeProviderStates(&snapshot)

	provider := normalizeProvider(snapshot.Settings.TargetType)
	state := providerState(snapshot, provider)
	displayNames := map[string]string{}
	providers := map[string]string{}
	for _, item := range state.Results {
		displayNames[item.FileName] = item.DisplayName
		providers[item.FileName] = normalizeProvider(item.Provider)
	}

	logs := make([]InspectionActionLog, 0, len(req.FileNames))
	for _, fileName := range req.FileNames {
		actionLog := InspectionActionLog{
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
		case ActionKeep, ActionReauth, ActionFailed:
		}
		if callErr != nil {
			actionLog.Success = false
			actionLog.Error = callErr.Error()
		}
		logs = append(logs, actionLog)
	}

	s.lock()
	defer s.unlock()

	latest, err := s.repo.Load(ctx)
	if err != nil {
		return ExecuteActionsResult{}, err
	}
	initializeProviderStates(&latest)
	state = providerState(latest, provider)
	for _, actionLog := range logs {
		if !actionLog.Success {
			continue
		}
		resultProvider := providers[actionLog.FileName]
		if resultProvider == "" {
			resultProvider = provider
		}
		switch req.Action {
		case ActionDisable:
			updateResultDisabled(state.Results, actionLog.FileName, true)
			setAutoDisabledFile(&latest, resultProvider, actionLog.FileName, false)
		case ActionEnable:
			updateResultDisabled(state.Results, actionLog.FileName, false)
			setAutoDisabledFile(&latest, resultProvider, actionLog.FileName, false)
		case ActionDelete:
			state.Results = deleteResultByFileName(state.Results, actionLog.FileName)
			setAutoDisabledFile(&latest, resultProvider, actionLog.FileName, false)
		}
	}
	state.ActionLogs = logs
	autoDeletedCount := state.Run.Summary.AutoDeletedCount
	state.Run.Summary = buildSummary(state.Results, len(state.Results))
	state.Run.Summary.AutoDeletedCount = autoDeletedCount
	setProviderState(&latest, provider, state)
	if err := s.repo.Save(ctx, latest); err != nil {
		return ExecuteActionsResult{}, err
	}

	return ExecuteActionsResult{Snapshot: snapshotForProvider(latest, provider), Logs: logs}, nil
}

func (s *Service) reconcileSnapshot(ctx context.Context, snapshot LatestSnapshot, provider string, files []AuthFileRecord) (LatestSnapshot, error) {
	current := make(map[string]AuthFileRecord, len(files))
	for _, file := range files {
		current[file.FileName] = file
	}

	changed := reconcileAutoDisabledFiles(&snapshot, provider, files)
	results := make([]InspectionResultItem, 0, len(snapshot.Results))
	for _, result := range snapshot.Results {
		file, ok := current[result.FileName]
		if !ok {
			changed = true
			continue
		}
		if result.Disabled != file.Disabled {
			result.Disabled = file.Disabled
			changed = true
		}
		if result.DisplayName != file.DisplayName && file.DisplayName != "" {
			result.DisplayName = file.DisplayName
			changed = true
		}
		normalized := resolveActionState(result)
		if normalized != result {
			result = normalized
			changed = true
		}
		results = append(results, result)
	}

	summary := buildSummary(results, len(files))
	summary.AutoDeletedCount = snapshot.Run.Summary.AutoDeletedCount
	if snapshot.Run.Summary != summary {
		changed = true
	}

	if !changed {
		return snapshot, nil
	}
	snapshot.Results = results
	snapshot.Run.Summary = summary
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return LatestSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) autoApplyScheduledActions(ctx context.Context, provider string, results []InspectionResultItem, snapshot *LatestSnapshot) ([]InspectionResultItem, []InspectionActionLog, int) {
	nextResults := make([]InspectionResultItem, 0, len(results))
	logs := make([]InspectionActionLog, 0)
	autoDeletedCount := 0

	for _, result := range results {
		resultProvider := normalizeProvider(result.Provider)
		if resultProvider == "" {
			resultProvider = normalizeProvider(provider)
		}
		switch result.Action {
		case ActionDelete:
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
				setAutoDisabledFile(snapshot, resultProvider, result.FileName, false)
			}
			logs = append(logs, log)
		case ActionDisable:
			log := InspectionActionLog{
				Action:       ActionDisable,
				FileName:     result.FileName,
				DisplayName:  result.DisplayName,
				Success:      true,
				ExecutedAtMS: nowMillis(),
			}
			if err := s.gateway.SetDisabled(ctx, result.FileName, true); err != nil {
				log.Success = false
				log.Error = err.Error()
				nextResults = append(nextResults, result)
			} else {
				result.Disabled = true
				if resultProvider == "xai" {
					setAutoDisabledFile(snapshot, resultProvider, result.FileName, true)
				}
				nextResults = append(nextResults, resolveActionState(result))
			}
			logs = append(logs, log)
		case ActionEnable:
			result, log := s.applyEnableAction(ctx, resultProvider, result, snapshot)
			nextResults = append(nextResults, result)
			logs = append(logs, log)
		default:
			nextResults = append(nextResults, result)
		}
	}

	return nextResults, logs, autoDeletedCount
}

func (s *Service) autoApplyXAIRecoveryActions(ctx context.Context, provider string, results []InspectionResultItem, snapshot *LatestSnapshot) ([]InspectionResultItem, []InspectionActionLog) {
	logs := make([]InspectionActionLog, 0)
	for i := range results {
		result := results[i]
		resultProvider := normalizeProvider(result.Provider)
		if resultProvider == "" {
			resultProvider = normalizeProvider(provider)
		}
		if resultProvider != "xai" || !result.Disabled || result.Action != ActionEnable || result.Error != "" || result.ActionReason != XAIProbeSucceededReason {
			continue
		}
		updatedResult, resultLog := s.applyEnableAction(ctx, resultProvider, result, snapshot)
		results[i] = updatedResult
		logs = append(logs, resultLog)
	}
	return results, logs
}

func (s *Service) applyEnableAction(ctx context.Context, provider string, result InspectionResultItem, snapshot *LatestSnapshot) (InspectionResultItem, InspectionActionLog) {
	log := InspectionActionLog{
		Action:       ActionEnable,
		FileName:     result.FileName,
		DisplayName:  result.DisplayName,
		Success:      true,
		ExecutedAtMS: nowMillis(),
	}
	if err := s.gateway.SetDisabled(ctx, result.FileName, false); err != nil {
		log.Success = false
		log.Error = err.Error()
		return result, log
	}
	result.Disabled = false
	setAutoDisabledFile(snapshot, provider, result.FileName, false)
	return resolveActionState(result), log
}

func applyXAIRecoveryActions(results []InspectionResultItem) []InspectionResultItem {
	for i := range results {
		result := &results[i]
		if normalizeProvider(result.Provider) != "xai" || !result.Disabled || result.Action != ActionKeep || result.Error != "" {
			continue
		}
		if result.ActionReason != XAIProbeSucceededDisabledReason {
			continue
		}
		result.Action = ActionEnable
		result.ActionReason = XAIProbeSucceededReason
		result.Executable = true
	}
	return results
}

func reconcileAutoDisabledFiles(snapshot *LatestSnapshot, provider string, files []AuthFileRecord) bool {
	if snapshot == nil {
		return false
	}
	provider = normalizeProvider(provider)
	markedFiles := snapshot.AutoDisabledFiles[provider]
	if len(markedFiles) == 0 {
		return false
	}

	current := make(map[string]bool, len(files))
	for _, file := range files {
		current[file.FileName] = file.Disabled
	}
	changed := false
	for fileName := range markedFiles {
		if !current[fileName] {
			delete(markedFiles, fileName)
			changed = true
		}
	}
	if len(markedFiles) == 0 {
		delete(snapshot.AutoDisabledFiles, provider)
	}
	return changed
}

func cloneAutoDisabledFiles(source map[string]map[string]bool) map[string]map[string]bool {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]map[string]bool, len(source))
	for provider, files := range source {
		cloned[provider] = maps.Clone(files)
	}
	return cloned
}

func replaceAutoDisabledFiles(snapshot *LatestSnapshot, provider string, files map[string]bool) {
	provider = normalizeProvider(provider)
	if snapshot == nil || provider == "" {
		return
	}
	if len(files) == 0 {
		delete(snapshot.AutoDisabledFiles, provider)
		return
	}
	if snapshot.AutoDisabledFiles == nil {
		snapshot.AutoDisabledFiles = make(map[string]map[string]bool)
	}
	snapshot.AutoDisabledFiles[provider] = maps.Clone(files)
}

func setAutoDisabledFile(snapshot *LatestSnapshot, provider string, fileName string, autoDisabled bool) {
	if snapshot == nil {
		return
	}
	provider = normalizeProvider(provider)
	fileName = strings.TrimSpace(fileName)
	if provider == "" || fileName == "" {
		return
	}
	if !autoDisabled {
		markedFiles := snapshot.AutoDisabledFiles[provider]
		delete(markedFiles, fileName)
		if len(markedFiles) == 0 {
			delete(snapshot.AutoDisabledFiles, provider)
		}
		return
	}
	if snapshot.AutoDisabledFiles == nil {
		snapshot.AutoDisabledFiles = make(map[string]map[string]bool)
	}
	if snapshot.AutoDisabledFiles[provider] == nil {
		snapshot.AutoDisabledFiles[provider] = make(map[string]bool)
	}
	snapshot.AutoDisabledFiles[provider][fileName] = true
}

func resolveActionState(result InspectionResultItem) InspectionResultItem {
	if result.Disabled && result.Action == ActionDisable {
		result.Action = ActionKeep
		result.ActionReason = "no issue detected"
		result.Executable = false
	}
	if !result.Disabled && result.Action == ActionEnable {
		result.Action = ActionKeep
		result.ActionReason = "no issue detected"
		result.Executable = false
	}
	return result
}

func updateResultDisabled(results []InspectionResultItem, fileName string, disabled bool) {
	for i := range results {
		if results[i].FileName == fileName {
			results[i].Disabled = disabled
			results[i] = resolveActionState(results[i])
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

func filterResultsByCurrentFiles(results []InspectionResultItem, files []AuthFileRecord) []InspectionResultItem {
	if len(results) == 0 {
		return results
	}

	current := make(map[string]struct{}, len(files))
	for _, file := range files {
		current[file.FileName] = struct{}{}
	}

	filtered := results[:0]
	for _, result := range results {
		if _, ok := current[result.FileName]; ok {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func filterAuthFilesByName(files []AuthFileRecord, wanted []string) []AuthFileRecord {
	if len(wanted) == 0 {
		return files
	}

	wantedSet := make(map[string]struct{}, len(wanted))
	for _, fileName := range wanted {
		wantedSet[fileName] = struct{}{}
	}

	filtered := make([]AuthFileRecord, 0, len(files))
	for _, file := range files {
		if _, ok := wantedSet[file.FileName]; ok {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func mergeRunResults(existing []InspectionResultItem, incoming []InspectionResultItem) []InspectionResultItem {
	if len(existing) == 0 {
		return incoming
	}
	if len(incoming) == 0 {
		return existing
	}

	incomingByFileName := make(map[string]InspectionResultItem, len(incoming))
	for _, item := range incoming {
		incomingByFileName[item.FileName] = item
	}

	merged := make([]InspectionResultItem, 0, len(existing)+len(incoming))
	for _, item := range existing {
		if replacement, ok := incomingByFileName[item.FileName]; ok {
			merged = append(merged, replacement)
			delete(incomingByFileName, item.FileName)
			continue
		}
		merged = append(merged, item)
	}
	for _, item := range incoming {
		if _, ok := incomingByFileName[item.FileName]; ok {
			merged = append(merged, item)
		}
	}
	return merged
}

func initializeProviderStates(snapshot *LatestSnapshot) {
	if snapshot == nil {
		return
	}
	if snapshot.ProviderStates == nil {
		snapshot.ProviderStates = make(map[string]ProviderInspectionState)
	}
	provider := normalizeProvider(snapshot.Settings.TargetType)
	if provider == "" {
		provider = DefaultSettings().TargetType
		snapshot.Settings.TargetType = provider
	}
	if _, exists := snapshot.ProviderStates[provider]; exists {
		return
	}
	run := snapshot.Run
	run.NextTriggerAtMSByProvider = nil
	snapshot.ProviderStates[provider] = ProviderInspectionState{
		Run:        run,
		Results:    append([]InspectionResultItem(nil), snapshot.Results...),
		ActionLogs: append([]InspectionActionLog(nil), snapshot.ActionLogs...),
	}
}

func persistSelectedProviderView(snapshot *LatestSnapshot) {
	if snapshot == nil {
		return
	}
	initializeProviderStates(snapshot)
	provider := normalizeProvider(snapshot.Settings.TargetType)
	run := snapshot.Run
	run.NextTriggerAtMSByProvider = nil
	state := snapshot.ProviderStates[provider]
	state.Run = run
	state.Results = append([]InspectionResultItem(nil), snapshot.Results...)
	state.ActionLogs = append([]InspectionActionLog(nil), snapshot.ActionLogs...)
	snapshot.ProviderStates[provider] = state
}

func syncSelectedProviderView(snapshot *LatestSnapshot) {
	if snapshot == nil {
		return
	}
	initializeProviderStates(snapshot)
	provider := normalizeProvider(snapshot.Settings.TargetType)
	state := snapshot.ProviderStates[provider]
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	snapshot.Run = state.Run
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
	snapshot.Results = append([]InspectionResultItem(nil), state.Results...)
	snapshot.ActionLogs = append([]InspectionActionLog(nil), state.ActionLogs...)
	if snapshot.Results == nil {
		snapshot.Results = []InspectionResultItem{}
	}
	if snapshot.ActionLogs == nil {
		snapshot.ActionLogs = []InspectionActionLog{}
	}
}

func selectProviderView(snapshot *LatestSnapshot, provider string) {
	if snapshot == nil {
		return
	}
	provider = normalizeProvider(provider)
	if provider == "" {
		provider = DefaultSettings().TargetType
	}
	persistSelectedProviderView(snapshot)
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	snapshot.Settings.TargetType = provider
	if _, exists := snapshot.ProviderStates[provider]; !exists {
		snapshot.ProviderStates[provider] = ProviderInspectionState{
			Run:        InspectionRunState{Status: RunStatusIdle},
			Results:    []InspectionResultItem{},
			ActionLogs: []InspectionActionLog{},
		}
	}
	syncSelectedProviderView(snapshot)
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
}

func providerState(snapshot LatestSnapshot, provider string) ProviderInspectionState {
	provider = normalizeProvider(provider)
	state, exists := snapshot.ProviderStates[provider]
	if !exists {
		return ProviderInspectionState{
			Run:        InspectionRunState{Status: RunStatusIdle},
			Results:    []InspectionResultItem{},
			ActionLogs: []InspectionActionLog{},
		}
	}
	if state.Results == nil {
		state.Results = []InspectionResultItem{}
	}
	if state.ActionLogs == nil {
		state.ActionLogs = []InspectionActionLog{}
	}
	return state
}

func setProviderState(snapshot *LatestSnapshot, provider string, state ProviderInspectionState) {
	if snapshot == nil {
		return
	}
	initializeProviderStates(snapshot)
	provider = normalizeProvider(provider)
	state.Run.NextTriggerAtMSByProvider = nil
	if state.Results == nil {
		state.Results = []InspectionResultItem{}
	}
	if state.ActionLogs == nil {
		state.ActionLogs = []InspectionActionLog{}
	}
	snapshot.ProviderStates[provider] = state
	if normalizeProvider(snapshot.Settings.TargetType) != provider {
		return
	}
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	snapshot.Run = state.Run
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
	snapshot.Results = append([]InspectionResultItem(nil), state.Results...)
	snapshot.ActionLogs = append([]InspectionActionLog(nil), state.ActionLogs...)
}

func snapshotForProvider(snapshot LatestSnapshot, provider string) LatestSnapshot {
	provider = normalizeProvider(provider)
	state := providerState(snapshot, provider)
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	snapshot.Settings.TargetType = provider
	snapshot.Run = state.Run
	snapshot.Run.NextTriggerAtMSByProvider = nextTriggers
	snapshot.Results = append([]InspectionResultItem(nil), state.Results...)
	snapshot.ActionLogs = append([]InspectionActionLog(nil), state.ActionLogs...)
	return snapshot
}

func inspectionBatchSize(provider string) int {
	if normalizeProvider(provider) == "xai" {
		return xaiInspectionBatchSize
	}
	return defaultInspectionBatchSize
}

func inspectionSettingsForProvider(settings InspectionSettings, provider string) InspectionSettings {
	settings.SampleSize = 0
	workerLimit := maxProviderWorkers
	switch normalizeProvider(provider) {
	case "codex":
		workerLimit = maxInspectionWorkers
	case "xai":
		workerLimit = maxXAIInspectionWorkers
	}
	if settings.Workers <= 0 {
		settings.Workers = 1
	}
	if settings.Workers > workerLimit {
		settings.Workers = workerLimit
	}
	return settings
}

func rotateAuthFiles(files []AuthFileRecord, cursor int) []AuthFileRecord {
	if len(files) == 0 {
		return []AuthFileRecord{}
	}
	cursor %= len(files)
	if cursor < 0 {
		cursor += len(files)
	}
	rotated := make([]AuthFileRecord, 0, len(files))
	rotated = append(rotated, files[cursor:]...)
	rotated = append(rotated, files[:cursor]...)
	return rotated
}

func waitInspectionBatch(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) lock() {
	s.mu.Lock()
}

func (s *Service) unlock() {
	s.mu.Unlock()
}

func updateProviderNextTrigger(snapshot *LatestSnapshot, provider string, triggerType TriggerType) {
	if snapshot == nil {
		return
	}
	provider = normalizeProvider(provider)
	if snapshot.Run.NextTriggerAtMSByProvider == nil {
		snapshot.Run.NextTriggerAtMSByProvider = make(map[string]int64)
	}
	schedule := snapshot.Settings.ScheduleFor(provider)
	if !schedule.Enabled || schedule.IntervalMinutes <= 0 {
		delete(snapshot.Run.NextTriggerAtMSByProvider, provider)
		return
	}
	if triggerType == TriggerTypeScheduled {
		snapshot.Run.NextTriggerAtMSByProvider[provider] = nowMillis() + int64(time.Duration(schedule.IntervalMinutes)*time.Minute/time.Millisecond)
	}
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
