package codexinspection

import "encoding/json"

type RunStatus string

const (
	RunStatusIdle      RunStatus = "idle"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
)

type TriggerType string

const (
	TriggerTypeManual    TriggerType = "manual"
	TriggerTypeScheduled TriggerType = "scheduled"
)

type Action string

const (
	ActionKeep    Action = "keep"
	ActionDelete  Action = "delete"
	ActionDisable Action = "disable"
	ActionEnable  Action = "enable"
	ActionReauth  Action = "reauth"
	ActionFailed  Action = "failed"
)

type InspectionSettings struct {
	TargetType                   string                        `json:"targetType"`
	Workers                      int                           `json:"workers"`
	TimeoutSeconds               int                           `json:"timeoutSeconds"`
	Retries                      int                           `json:"retries"`
	SampleSize                   int                           `json:"sampleSize"`
	FiveHourUsedPercentThreshold int                           `json:"fiveHourUsedPercentThreshold"`
	WeeklyUsedPercentThreshold   int                           `json:"weeklyUsedPercentThreshold"`
	StatusCodeActions            map[string]map[int]Action     `json:"statusCodeActions,omitempty"`
	Schedules                    map[string]InspectionSchedule `json:"schedules,omitempty"`
}

type inspectionSettingsAlias InspectionSettings

func (s *InspectionSettings) UnmarshalJSON(data []byte) error {
	aux := struct {
		inspectionSettingsAlias
		UsedPercentThreshold *int                `json:"usedPercentThreshold"`
		Schedule             *InspectionSchedule `json:"schedule"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = InspectionSettings(aux.inspectionSettingsAlias)
	if aux.UsedPercentThreshold != nil {
		if s.FiveHourUsedPercentThreshold == 0 {
			s.FiveHourUsedPercentThreshold = *aux.UsedPercentThreshold
		}
		if s.WeeklyUsedPercentThreshold == 0 {
			s.WeeklyUsedPercentThreshold = *aux.UsedPercentThreshold
		}
	}
	s.Schedules = normalizeSchedules(s.Schedules)
	if aux.Schedule != nil {
		provider := normalizeProvider(s.TargetType)
		if provider == "" {
			provider = DefaultSettings().TargetType
		}
		if _, exists := s.Schedules[provider]; !exists {
			s.SetSchedule(provider, *aux.Schedule)
		}
	}
	return nil
}

type InspectionSchedule struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"intervalMinutes"`
}

func DefaultSchedule() InspectionSchedule {
	return InspectionSchedule{
		Enabled:         false,
		Mode:            "interval",
		IntervalMinutes: 60,
	}
}

func (s InspectionSettings) ScheduleFor(provider string) InspectionSchedule {
	provider = normalizeProvider(provider)
	if schedule, ok := s.Schedules[provider]; ok {
		if schedule.Mode == "" {
			schedule.Mode = DefaultSchedule().Mode
		}
		return schedule
	}
	return DefaultSchedule()
}

func (s *InspectionSettings) SetSchedule(provider string, schedule InspectionSchedule) {
	provider = normalizeProvider(provider)
	if provider == "" {
		return
	}
	if schedule.Mode == "" {
		schedule.Mode = DefaultSchedule().Mode
	}
	if s.Schedules == nil {
		s.Schedules = make(map[string]InspectionSchedule)
	}
	s.Schedules[provider] = schedule
}

func normalizeSchedules(schedules map[string]InspectionSchedule) map[string]InspectionSchedule {
	if len(schedules) == 0 {
		return nil
	}
	normalized := make(map[string]InspectionSchedule, len(schedules))
	for provider, schedule := range schedules {
		provider = normalizeProvider(provider)
		if provider == "" {
			continue
		}
		if schedule.Mode == "" {
			schedule.Mode = DefaultSchedule().Mode
		}
		normalized[provider] = schedule
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeNextTriggers(nextTriggers map[string]int64) map[string]int64 {
	if len(nextTriggers) == 0 {
		return nil
	}
	normalized := make(map[string]int64, len(nextTriggers))
	for provider, nextTriggerAtMS := range nextTriggers {
		provider = normalizeProvider(provider)
		if provider == "" || nextTriggerAtMS <= 0 {
			continue
		}
		normalized[provider] = nextTriggerAtMS
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

type InspectionSummary struct {
	TotalFiles       int `json:"totalFiles"`
	SampledCount     int `json:"sampledCount"`
	KeepCount        int `json:"keepCount"`
	DeleteCount      int `json:"deleteCount"`
	DisableCount     int `json:"disableCount"`
	EnableCount      int `json:"enableCount"`
	ReauthCount      int `json:"reauthCount"`
	FailedCount      int `json:"failedCount"`
	DisabledCount    int `json:"disabledCount"`
	EnabledCount     int `json:"enabledCount"`
	AutoDeletedCount int `json:"autoDeletedCount"`
}

type InspectionRunState struct {
	Status                    RunStatus         `json:"status"`
	TriggerType               TriggerType       `json:"triggerType"`
	StartedAtMS               int64             `json:"startedAtMs"`
	FinishedAtMS              int64             `json:"finishedAtMs"`
	NextTriggerAtMSByProvider map[string]int64  `json:"nextTriggerAtMsByProvider,omitempty"`
	Summary                   InspectionSummary `json:"summary"`
	Error                     string            `json:"error,omitempty"`
	legacyNextTriggerAtMS     int64
}

type inspectionRunStateAlias InspectionRunState

func (s *InspectionRunState) UnmarshalJSON(data []byte) error {
	aux := struct {
		inspectionRunStateAlias
		NextTriggerAtMS int64 `json:"nextTriggerAtMs"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = InspectionRunState(aux.inspectionRunStateAlias)
	s.NextTriggerAtMSByProvider = normalizeNextTriggers(s.NextTriggerAtMSByProvider)
	s.legacyNextTriggerAtMS = aux.NextTriggerAtMS
	return nil
}

type InspectionResultItem struct {
	FileName            string `json:"fileName"`
	DisplayName         string `json:"displayName"`
	Provider            string `json:"provider"`
	AuthIndex           string `json:"authIndex,omitempty"`
	AccountID           string `json:"accountId,omitempty"`
	Disabled            bool   `json:"disabled"`
	StatusCode          int    `json:"statusCode,omitempty"`
	UsedPercent         *int   `json:"usedPercent,omitempty"`
	FiveHourUsedPercent *int   `json:"fiveHourUsedPercent,omitempty"`
	WeeklyUsedPercent   *int   `json:"weeklyUsedPercent,omitempty"`
	Error               string `json:"error,omitempty"`
	Action              Action `json:"action"`
	ActionReason        string `json:"actionReason"`
	Executable          bool   `json:"executable"`
}

type InspectionActionLog struct {
	Action       Action `json:"action"`
	FileName     string `json:"fileName"`
	DisplayName  string `json:"displayName"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
	ExecutedAtMS int64  `json:"executedAtMs"`
}

type LatestSnapshot struct {
	Settings   InspectionSettings     `json:"settings"`
	Run        InspectionRunState     `json:"run"`
	Results    []InspectionResultItem `json:"results"`
	ActionLogs []InspectionActionLog  `json:"actionLogs"`
}

func DefaultSettings() InspectionSettings {
	return InspectionSettings{
		TargetType:                   "codex",
		Workers:                      4,
		TimeoutSeconds:               20,
		Retries:                      1,
		SampleSize:                   0,
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
		Schedules: map[string]InspectionSchedule{
			"codex": DefaultSchedule(),
		},
	}
}

func DefaultSnapshot() LatestSnapshot {
	return LatestSnapshot{
		Settings:   DefaultSettings(),
		Run:        InspectionRunState{Status: RunStatusIdle},
		Results:    []InspectionResultItem{},
		ActionLogs: []InspectionActionLog{},
	}
}

func applyDefaultSettings(settings InspectionSettings) InspectionSettings {
	if settings.TargetType == "" {
		return DefaultSettings()
	}
	settings.TargetType = normalizeProvider(settings.TargetType)
	settings.Schedules = normalizeSchedules(settings.Schedules)
	return settings
}
