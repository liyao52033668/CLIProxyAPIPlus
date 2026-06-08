package codexinspection

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
)

type InspectionSettings struct {
	TargetType           string             `json:"targetType"`
	Workers              int                `json:"workers"`
	TimeoutSeconds       int                `json:"timeoutSeconds"`
	Retries              int                `json:"retries"`
	SampleSize           int                `json:"sampleSize"`
	UsedPercentThreshold int                `json:"usedPercentThreshold"`
	Schedule             InspectionSchedule `json:"schedule"`
}

type InspectionSchedule struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"intervalMinutes"`
}

type InspectionSummary struct {
	TotalFiles       int `json:"totalFiles"`
	SampledCount     int `json:"sampledCount"`
	KeepCount        int `json:"keepCount"`
	DeleteCount      int `json:"deleteCount"`
	DisableCount     int `json:"disableCount"`
	EnableCount      int `json:"enableCount"`
	ReauthCount      int `json:"reauthCount"`
	DisabledCount    int `json:"disabledCount"`
	EnabledCount     int `json:"enabledCount"`
	AutoDeletedCount int `json:"autoDeletedCount"`
}

type InspectionRunState struct {
	Status          RunStatus         `json:"status"`
	TriggerType     TriggerType       `json:"triggerType"`
	StartedAtMS     int64             `json:"startedAtMs"`
	FinishedAtMS    int64             `json:"finishedAtMs"`
	NextTriggerAtMS int64             `json:"nextTriggerAtMs,omitempty"`
	Summary         InspectionSummary `json:"summary"`
	Error           string            `json:"error,omitempty"`
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
		TargetType:           "codex",
		Workers:              4,
		TimeoutSeconds:       20,
		Retries:              1,
		SampleSize:           0,
		UsedPercentThreshold: 85,
		Schedule: InspectionSchedule{
			Enabled:         false,
			Mode:            "interval",
			IntervalMinutes: 60,
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
	if settings.TargetType != "" {
		return settings
	}
	return DefaultSettings()
}
