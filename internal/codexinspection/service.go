package codexinspection

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"
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

func (s *Service) GetSnapshot(ctx context.Context) (LatestSnapshot, error) {
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return LatestSnapshot{}, err
	}
	return s.reconcileSnapshot(ctx, snapshot)
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

	provider := normalizeProvider(req.Provider)
	if provider == "" {
		provider = normalizeProvider(snapshot.Settings.TargetType)
	}
	if provider == "" {
		provider = DefaultSettings().TargetType
	}
	if !strings.EqualFold(snapshot.Settings.TargetType, provider) {
		snapshot.Results = []InspectionResultItem{}
		snapshot.ActionLogs = []InspectionActionLog{}
	}
	snapshot.Settings.TargetType = provider

	startedAtMS := nowMillis()
	nextTriggers := maps.Clone(snapshot.Run.NextTriggerAtMSByProvider)
	if nextTriggers == nil {
		nextTriggers = make(map[string]int64)
	}
	if schedule := snapshot.Settings.ScheduleFor(provider); !schedule.Enabled || schedule.IntervalMinutes <= 0 {
		delete(nextTriggers, provider)
	}
	snapshot.Run = InspectionRunState{
		Status:                    RunStatusRunning,
		TriggerType:               req.TriggerType,
		StartedAtMS:               startedAtMS,
		NextTriggerAtMSByProvider: nextTriggers,
	}
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return LatestSnapshot{}, err
	}

	files, err := s.gateway.ListAuthFiles(ctx, provider)
	if err != nil {
		return LatestSnapshot{}, err
	}
	reconcileAutoDisabledFiles(&snapshot, provider, files)
	probeFiles := filterAuthFilesByName(files, req.FileNames)
	if len(req.FileNames) > 0 {
		snapshot.Results = filterResultsByCurrentFiles(snapshot.Results, files)
	}

	results, probeErr := s.prober.ProbeAccounts(ctx, probeFiles, snapshot.Settings)
	results = applyXAIRecoveryActions(results)
	if probeErr != nil {
		if len(req.FileNames) == 0 {
			snapshot.Results = results
		} else {
			snapshot.Results = mergeRunResults(snapshot.Results, results)
		}
		snapshot.Run.Status = RunStatusFailed
		snapshot.Run.FinishedAtMS = nowMillis()
		snapshot.Run.Error = probeErr.Error()
		snapshot.Run.Summary = buildSummary(snapshot.Results, len(files))
		updateProviderNextTrigger(&snapshot, provider, req.TriggerType)
		if saveErr := s.repo.Save(ctx, snapshot); saveErr != nil {
			return LatestSnapshot{}, saveErr
		}
		return snapshot, probeErr
	}

	autoActionLogs := []InspectionActionLog{}
	autoDeletedCount := 0
	if req.TriggerType == TriggerTypeScheduled {
		results, autoActionLogs, autoDeletedCount = s.autoApplyScheduledActions(ctx, provider, results, &snapshot)
	} else {
		results, autoActionLogs = s.autoApplyXAIRecoveryActions(ctx, provider, results, &snapshot)
	}

	if len(req.FileNames) == 0 {
		snapshot.Results = results
	} else {
		snapshot.Results = mergeRunResults(snapshot.Results, results)
	}
	snapshot.ActionLogs = autoActionLogs
	snapshot.Run.Status = RunStatusCompleted
	snapshot.Run.FinishedAtMS = nowMillis()
	snapshot.Run.Error = ""
	snapshot.Run.Summary = buildSummary(snapshot.Results, len(files))
	snapshot.Run.Summary.AutoDeletedCount = autoDeletedCount
	updateProviderNextTrigger(&snapshot, provider, req.TriggerType)
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
	providers := map[string]string{}
	for _, item := range snapshot.Results {
		displayNames[item.FileName] = item.DisplayName
		providers[item.FileName] = normalizeProvider(item.Provider)
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
		case ActionKeep, ActionReauth, ActionFailed:
		}

		if callErr != nil {
			log.Success = false
			log.Error = callErr.Error()
		} else {
			provider := providers[fileName]
			if provider == "" {
				provider = normalizeProvider(snapshot.Settings.TargetType)
			}
			switch req.Action {
			case ActionDisable:
				updateResultDisabled(snapshot.Results, fileName, true)
				setAutoDisabledFile(&snapshot, provider, fileName, false)
			case ActionEnable:
				updateResultDisabled(snapshot.Results, fileName, false)
				setAutoDisabledFile(&snapshot, provider, fileName, false)
			case ActionDelete:
				snapshot.Results = deleteResultByFileName(snapshot.Results, fileName)
				setAutoDisabledFile(&snapshot, provider, fileName, false)
			}
		}
		logs = append(logs, log)
	}

	snapshot.ActionLogs = logs
	autoDeletedCount := snapshot.Run.Summary.AutoDeletedCount
	snapshot.Run.Summary = buildSummary(snapshot.Results, len(snapshot.Results))
	snapshot.Run.Summary.AutoDeletedCount = autoDeletedCount
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return ExecuteActionsResult{}, err
	}

	return ExecuteActionsResult{Snapshot: snapshot, Logs: logs}, nil
}

func (s *Service) reconcileSnapshot(ctx context.Context, snapshot LatestSnapshot) (LatestSnapshot, error) {
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

func (s *Service) lock() {
	s.mu <- struct{}{}
}

func (s *Service) unlock() {
	<-s.mu
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
