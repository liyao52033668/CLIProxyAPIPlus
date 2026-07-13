package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
)

func TestCodexInspectionActionForUsagePrefersWeeklyWindow(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHour := 92
	weeklySafe := 40

	// Weekly safe + 5h exceeded: still suggest disable (added on top of original weekly preference).
	action, reason := codexInspectionActionForUsage(false, &fiveHour, &weeklySafe, settings)
	if action != codexinspection.ActionDisable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDisable)
	}
	if reason != "fiveHourUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "fiveHourUsedPercent >= 85")
	}

	// Weekly exceeded has highest priority and suggests delete.
	weeklyExceeded := 88
	action, reason = codexInspectionActionForUsage(false, &fiveHour, &weeklyExceeded, settings)
	if action != codexinspection.ActionDelete {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDelete)
	}
	if reason != "weeklyUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "weeklyUsedPercent >= 85")
	}
}

func TestCodexInspectionActionForUsageFallsBackToFiveHourWindow(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHourExceeded := 90

	action, reason := codexInspectionActionForUsage(false, &fiveHourExceeded, nil, settings)
	if action != codexinspection.ActionDisable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDisable)
	}
	if reason != "fiveHourUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "fiveHourUsedPercent >= 85")
	}

	// Missing weekly window must not enable, even if 5h has recovered.
	fiveHourSafe := 30
	action, reason = codexInspectionActionForUsage(true, &fiveHourSafe, nil, settings)
	if action != codexinspection.ActionKeep {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionKeep)
	}
	if reason != "no issue detected" {
		t.Fatalf("reason = %q, want %q", reason, "no issue detected")
	}
}

func TestCodexInspectionActionForUsageEnablesOnlyWhenBothWindowsRecovered(t *testing.T) {
	settings := codexinspection.InspectionSettings{
		FiveHourUsedPercentThreshold: 85,
		WeeklyUsedPercentThreshold:   85,
	}
	fiveHourExceeded := 99
	weeklySafe := 40
	fiveHourSafe := 20

	// 5h still high + weekly recovered → keep disabled.
	action, reason := codexInspectionActionForUsage(true, &fiveHourExceeded, &weeklySafe, settings)
	if action != codexinspection.ActionKeep {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionKeep)
	}
	if reason != "no issue detected" {
		t.Fatalf("reason = %q, want %q", reason, "no issue detected")
	}

	// Weekly exceeded still suggests delete for disabled accounts.
	weeklyExceeded := 90
	action, reason = codexInspectionActionForUsage(true, &fiveHourExceeded, &weeklyExceeded, settings)
	if action != codexinspection.ActionDelete {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionDelete)
	}
	if reason != "weeklyUsedPercent >= 85" {
		t.Fatalf("reason = %q, want %q", reason, "weeklyUsedPercent >= 85")
	}

	// Both recovered → enable.
	action, reason = codexInspectionActionForUsage(true, &fiveHourSafe, &weeklySafe, settings)
	if action != codexinspection.ActionEnable {
		t.Fatalf("action = %q, want %q", action, codexinspection.ActionEnable)
	}
	if reason != "fiveHourUsedPercent < 85 && weeklyUsedPercent < 85" {
		t.Fatalf("reason = %q, want both windows recovered", reason)
	}
}

func TestCodexInspectionUsagePercentsPrefersWeeklyForUsedPercent(t *testing.T) {
	payload := &quota.CodexUsagePayload{
		RateLimit: &quota.CodexRateLimitInfo{
			PrimaryWindow:   &quota.CodexUsageWindow{UsedPercent: 92, LimitWindowSeconds: codexInspectionFiveHourSeconds},
			SecondaryWindow: &quota.CodexUsageWindow{UsedPercent: 48, LimitWindowSeconds: codexInspectionWeekSeconds},
		},
	}

	fiveHour, weekly, usedPercent, ok := codexInspectionUsagePercents(payload)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if fiveHour == nil || *fiveHour != 92 {
		t.Fatalf("fiveHour = %v, want 92", fiveHour)
	}
	if weekly == nil || *weekly != 48 {
		t.Fatalf("weekly = %v, want 48", weekly)
	}
	if usedPercent == nil || *usedPercent != 48 {
		t.Fatalf("usedPercent = %v, want 48", usedPercent)
	}
}

func TestCodexInspectionUsagePercentsFallsBackByWindowPresence(t *testing.T) {
	payload := &quota.CodexUsagePayload{
		RateLimit: &quota.CodexRateLimitInfo{
			PrimaryWindow: &quota.CodexUsageWindow{UsedPercent: 77, LimitWindowSeconds: codexInspectionFiveHourSeconds},
		},
	}

	fiveHour, weekly, usedPercent, ok := codexInspectionUsagePercents(payload)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if fiveHour == nil || *fiveHour != 77 {
		t.Fatalf("fiveHour = %v, want 77", fiveHour)
	}
	if weekly != nil {
		t.Fatalf("weekly = %v, want nil", weekly)
	}
	if usedPercent == nil || *usedPercent != 77 {
		t.Fatalf("usedPercent = %v, want 77", usedPercent)
	}
}
