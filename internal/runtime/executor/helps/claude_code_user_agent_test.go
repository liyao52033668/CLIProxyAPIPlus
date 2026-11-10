package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func claudeUATestHeaders(userAgent string) http.Header {
	headers := http.Header{}
	if userAgent != "" {
		headers.Set("User-Agent", userAgent)
	}
	return headers
}

func TestParseClaudeCodeUserAgent(t *testing.T) {
	tests := []struct {
		name           string
		userAgent      string
		wantOK         bool
		wantEntrypoint string
		wantAgentSDK   string
		wantProfileUA  string
	}{
		{
			name:           "native cli",
			userAgent:      "claude-cli/2.1.321 (external, cli)",
			wantOK:         true,
			wantEntrypoint: "cli",
			wantProfileUA:  "claude-cli/2.1.321 (external, cli)",
		},
		{
			name:           "agent-sdk is stable identity",
			userAgent:      "claude-cli/2.1.321 (external, cli, agent-sdk/0.128.0)",
			wantOK:         true,
			wantEntrypoint: "cli",
			wantAgentSDK:   "0.128.0",
			wantProfileUA:  "claude-cli/2.1.321 (external, cli, agent-sdk/0.128.0)",
		},
		{
			name:           "request extension fields stay out of the profile",
			userAgent:      "claude-cli/2.1.321 (external, cli, workload/cron, agent-sdk/0.128.0, future-feature/value)",
			wantOK:         true,
			wantEntrypoint: "cli",
			wantAgentSDK:   "0.128.0",
			wantProfileUA:  "claude-cli/2.1.321 (external, cli, agent-sdk/0.128.0)",
		},
		{
			name:           "sdk-cli entrypoint",
			userAgent:      "claude-cli/2.1.321 (external, sdk-cli)",
			wantOK:         true,
			wantEntrypoint: "sdk-cli",
			wantProfileUA:  "claude-cli/2.1.321 (external, sdk-cli)",
		},
		{name: "empty", userAgent: "", wantOK: false},
		{name: "not claude code", userAgent: "curl/8.0", wantOK: false},
		{name: "missing comment", userAgent: "claude-cli/2.1.321", wantOK: false},
		{name: "non external", userAgent: "claude-cli/2.1.321 (internal, cli)", wantOK: false},
		{name: "missing entrypoint", userAgent: "claude-cli/2.1.321 (external)", wantOK: false},
		{name: "field without slash", userAgent: "claude-cli/2.1.321 (external, cli, workload)", wantOK: false},
		{name: "duplicate field", userAgent: "claude-cli/2.1.321 (external, cli, workload/a, workload/b)", wantOK: false},
		{name: "control characters", userAgent: "claude-cli/2.1.321 (external, cli)\x00", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, ok := parseClaudeCodeUserAgent(tc.userAgent)
			if ok != tc.wantOK {
				t.Fatalf("parseClaudeCodeUserAgent(%q) ok = %v, want %v", tc.userAgent, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if parsed.entrypoint != tc.wantEntrypoint {
				t.Errorf("entrypoint = %q, want %q", parsed.entrypoint, tc.wantEntrypoint)
			}
			if parsed.agentSDKVersion != tc.wantAgentSDK {
				t.Errorf("agentSDKVersion = %q, want %q", parsed.agentSDKVersion, tc.wantAgentSDK)
			}
			if parsed.profileUserAgent != tc.wantProfileUA {
				t.Errorf("profileUserAgent = %q, want %q", parsed.profileUserAgent, tc.wantProfileUA)
			}
		})
	}
}

func TestExtractClaudeDeviceProfileDropsRequestExtensions(t *testing.T) {
	headers := claudeUATestHeaders("claude-cli/2.1.321 (external, cli, workload/cron, agent-sdk/0.128.0)")
	profile, ok := extractClaudeDeviceProfile(headers, &config.Config{})
	if !ok {
		t.Fatal("extractClaudeDeviceProfile() ok = false, want true")
	}
	if want := "claude-cli/2.1.321 (external, cli, agent-sdk/0.128.0)"; profile.UserAgent != want {
		t.Errorf("UserAgent = %q, want %q", profile.UserAgent, want)
	}
}

func TestExtractClaudeDeviceProfileRejectsPlainClaudeCLIProduct(t *testing.T) {
	// A bare product token without the required (external, entrypoint) comment is
	// not a native Claude Code UA and must not seed a device profile.
	headers := claudeUATestHeaders("claude-cli/2.1.321")
	if _, ok := extractClaudeDeviceProfile(headers, &config.Config{}); ok {
		t.Fatal("extractClaudeDeviceProfile() ok = true, want false")
	}
}

func TestApplyClaudeDeviceProfileHeadersKeepsBaselineIdentityAndRequestFields(t *testing.T) {
	profile := defaultClaudeDeviceProfile(&config.Config{})
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	ApplyClaudeDeviceProfileHeaders(req, profile, "claude-cli/2.1.330 (external, cli, workload/cron, agent-sdk/0.128.0)")

	// The software identity stays on the baseline; only the caller's extension
	// fields ride along on this request. The request's agent-sdk value is not
	// inherited because it is not a request-scoped field.
	want := "claude-cli/2.1.321 (external, cli, workload/cron)"
	if got := req.Header.Get("User-Agent"); got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
	if got := req.Header.Get("X-Stainless-Package-Version"); got != defaultClaudeFingerprintPackageVersion {
		t.Errorf("X-Stainless-Package-Version = %q, want %q", got, defaultClaudeFingerprintPackageVersion)
	}
}

func TestApplyClaudeDeviceProfileHeadersWithoutRequestUserAgent(t *testing.T) {
	profile := defaultClaudeDeviceProfile(&config.Config{})
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	ApplyClaudeDeviceProfileHeaders(req, profile)
	if got := req.Header.Get("User-Agent"); got != defaultClaudeFingerprintUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, defaultClaudeFingerprintUserAgent)
	}
}

func TestDefaultClaudeFingerprintBaseline(t *testing.T) {
	profile := defaultClaudeDeviceProfile(&config.Config{})
	if want := "claude-cli/2.1.321 (external, cli)"; profile.UserAgent != want {
		t.Errorf("UserAgent = %q, want %q", profile.UserAgent, want)
	}
	if defaultClaudeFingerprintPackageVersion != "0.128.0" {
		t.Errorf("package version = %q, want 0.128.0", defaultClaudeFingerprintPackageVersion)
	}
	if defaultClaudeFingerprintRuntimeVersion != "v26.12.1" {
		t.Errorf("runtime version = %q, want v26.12.1", defaultClaudeFingerprintRuntimeVersion)
	}
	if got := DefaultClaudeVersion(nil); got != "2.1.321" {
		t.Errorf("DefaultClaudeVersion() = %q, want 2.1.321", got)
	}
}

func TestPlausibleClaudeCLIVersion(t *testing.T) {
	baseline := claudeCLIVersion{major: 2, minor: 1, patch: 321}
	tests := []struct {
		candidate claudeCLIVersion
		want      bool
	}{
		{claudeCLIVersion{major: 2, minor: 1, patch: 321}, true},
		{claudeCLIVersion{major: 2, minor: 1, patch: 330}, true},
		{claudeCLIVersion{major: 2, minor: 1, patch: 320}, false},
		{claudeCLIVersion{major: 2, minor: 2, patch: 0}, false},
		{claudeCLIVersion{major: 1, minor: 9, patch: 999}, false},
	}
	for _, tc := range tests {
		if got := plausibleClaudeCLIVersion(tc.candidate, baseline); got != tc.want {
			t.Errorf("plausibleClaudeCLIVersion(%v) = %v, want %v", tc.candidate, got, tc.want)
		}
	}
}
