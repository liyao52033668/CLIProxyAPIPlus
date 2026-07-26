package authfiles

import "time"

// AuthFilesResponse is the CPA /management/auth-files response DTO.
type AuthFilesResponse struct {
	Files []AuthFile `json:"files"`
}

// AuthFile is the raw response DTO for a single auth file from CPA /management/auth-files.
type AuthFile struct {
	AuthIndex      string           `json:"auth_index"`
	Name           string           `json:"name"`
	Email          string           `json:"email"`
	Type           string           `json:"type"`
	Provider       string           `json:"provider"`
	Label          string           `json:"label"`
	Status         string           `json:"status"`
	Source         string           `json:"source"`
	Disabled       bool             `json:"disabled"`
	Unavailable    bool             `json:"unavailable"`
	RuntimeOnly    bool             `json:"runtime_only"`
	Account        string           `json:"account,omitempty"`
	Metadata       map[string]any   `json:"metadata,omitempty"`
	Attributes     map[string]any   `json:"attributes,omitempty"`
	ProjectID      string           `json:"project_id,omitempty"`
	ProjectIDCamel string           `json:"projectId,omitempty"`
	IDToken        *AuthFileIDToken `json:"id_token"`
}

// AuthFileIDToken is the id_token subscription metadata DTO for a Codex auth file.
type AuthFileIDToken struct {
	AccountID        *string    `json:"chatgpt_account_id,omitempty"`
	AccountIDCamel   *string    `json:"chatgptAccountId,omitempty"`
	ActiveStart      *time.Time `json:"chatgpt_subscription_active_start,omitempty"`
	ActiveStartCamel *time.Time `json:"chatgptSubscriptionActiveStart,omitempty"`
	ActiveUntil      *time.Time `json:"chatgpt_subscription_active_until,omitempty"`
	ActiveUntilCamel *time.Time `json:"chatgptSubscriptionActiveUntil,omitempty"`
	PlanType         *string    `json:"plan_type,omitempty"`
	PlanTypeCamel    *string    `json:"planType,omitempty"`
}
