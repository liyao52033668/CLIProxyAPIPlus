package quota

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func NormalizeQuotaRows(output ProviderOutput) []QuotaRow {
	// Do not force a common raw shape at the provider layer; convert to frontend quota rows only at the export boundary.
	switch result := output.Result.(type) {
	case AntigravityResult:
		return normalizeAntigravityQuotaRows(result)
	case *AntigravityResult:
		if result == nil {
			return nil
		}
		return normalizeAntigravityQuotaRows(*result)
	case CodexResult:
		return normalizeCodexQuotaRows(result)
	case *CodexResult:
		if result == nil {
			return nil
		}
		return normalizeCodexQuotaRows(*result)
	case GeminiCLIResult:
		return normalizeGeminiCLIQuotaRows(result)
	case *GeminiCLIResult:
		if result == nil {
			return nil
		}
		return normalizeGeminiCLIQuotaRows(*result)
	case ClaudeResult:
		return normalizeClaudeQuotaRows(result)
	case *ClaudeResult:
		if result == nil {
			return nil
		}
		return normalizeClaudeQuotaRows(*result)
	case KimiResult:
		return normalizeKimiQuotaRows(result)
	case *KimiResult:
		if result == nil {
			return nil
		}
		return normalizeKimiQuotaRows(*result)
	case QoderResult:
		return normalizeQoderQuotaRows(result)
	case *QoderResult:
		if result == nil {
			return nil
		}
		return normalizeQoderQuotaRows(*result)
	case CommandCodeResult:
		return normalizeCommandCodeQuotaRows(result)
	case *CommandCodeResult:
		if result == nil {
			return nil
		}
		return normalizeCommandCodeQuotaRows(*result)
	default:
		return nil
	}
}

func normalizeClaudeQuotaRows(result ClaudeResult) []QuotaRow {
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 8)
	rows = appendClaudeWindowQuotaRow(rows, "five_hour", "5h", "window", result.Usage.FiveHour)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day", "Weekly", "window", result.Usage.SevenDay)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_oauth_apps", "7d OAuth Apps", "window", result.Usage.SevenDayOAuthApps)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_opus", "7d Opus", "model", result.Usage.SevenDayOpus)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_sonnet", "7d Sonnet", "model", result.Usage.SevenDaySonnet)
	rows = appendClaudeWindowQuotaRow(rows, "seven_day_cowork", "7d Cowork", "window", result.Usage.SevenDayCowork)
	rows = appendClaudeWindowQuotaRow(rows, "iguana_necktie", "Iguana Necktie", "window", result.Usage.IguanaNecktie)
	if result.Usage.ExtraUsage != nil {
		rows = append(rows, QuotaRow{
			Key:         "extra_usage",
			Label:       "Extra Usage",
			Scope:       "extra_usage",
			Used:        floatPtr(result.Usage.ExtraUsage.UsedCredits),
			Limit:       floatPtr(result.Usage.ExtraUsage.MonthlyLimit),
			UsedPercent: result.Usage.ExtraUsage.Utilization,
			Allowed:     boolPtr(result.Usage.ExtraUsage.IsEnabled),
		})
	}
	return rows
}

func appendClaudeWindowQuotaRow(rows []QuotaRow, key string, label string, scope string, window *ClaudeUsageWindow) []QuotaRow {
	if window == nil {
		return rows
	}
	return append(rows, QuotaRow{
		Key:         key,
		Label:       label,
		Scope:       scope,
		UsedPercent: floatPtr(window.Utilization),
		ResetAt:     window.ResetsAt,
	})
}

func normalizeCodexQuotaRows(result CodexResult) []QuotaRow {
	// Codex distinguishes 5h/Weekly via limit_window_seconds; unknown windows are labeled Window without guessing.
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 4+len(result.Usage.AdditionalRateLimits)*2)
	rows = appendCodexWindowQuotaRows(rows, "rate_limit", "5h", "Weekly", "window", "", result.Usage.RateLimit)
	rows = appendCodexWindowQuotaRows(rows, "code_review_rate_limit", "Code Review 5h", "Code Review Weekly", "code_review", "", result.Usage.CodeReviewRateLimit)
	for _, additional := range result.Usage.AdditionalRateLimits {
		// Keep non-primary windows such as code review / spark as extra quota so upstream data is not dropped.
		metric := additional.MeteredFeature
		if metric == "" {
			metric = additional.LimitName
		}
		primaryLabel := additional.LimitName + " 5h"
		secondaryLabel := additional.LimitName + " Weekly"
		rows = appendCodexWindowQuotaRows(rows, "additional_rate_limits."+additional.LimitName, primaryLabel, secondaryLabel, "additional", metric, additional.RateLimit)
	}
	return rows
}

func appendCodexWindowQuotaRows(rows []QuotaRow, keyPrefix string, primaryLabel string, secondaryLabel string, scope string, metric string, info *CodexRateLimitInfo) []QuotaRow {
	if info == nil {
		return rows
	}
	rows = appendCodexWindowQuotaRow(rows, keyPrefix+".primary_window", primaryLabel, scope, metric, info, info.PrimaryWindow)
	rows = appendCodexWindowQuotaRow(rows, keyPrefix+".secondary_window", secondaryLabel, scope, metric, info, info.SecondaryWindow)
	return rows
}

func codexWindowLabel(label string, seconds int64) string {
	switch seconds {
	case 18000:
		if strings.Contains(label, "Weekly") {
			return strings.Replace(label, "Weekly", "5h", 1)
		}
		return label
	case 604800:
		if strings.Contains(label, "5h") {
			return strings.Replace(label, "5h", "Weekly", 1)
		}
		return label
	}
	return codexUnknownWindowLabel(label)
}

func codexUnknownWindowLabel(label string) string {
	if label == "5h" || label == "Weekly" {
		return "Window"
	}
	if strings.Contains(label, "5h") {
		return strings.Replace(label, "5h", "Window", 1)
	}
	if strings.Contains(label, "Weekly") {
		return strings.Replace(label, "Weekly", "Window", 1)
	}
	return label
}

func appendCodexWindowQuotaRow(rows []QuotaRow, key string, label string, scope string, metric string, info *CodexRateLimitInfo, window *CodexUsageWindow) []QuotaRow {
	// Expand each window into its own quota row; the frontend decides primary/secondary placement by window seconds.
	if window == nil {
		return rows
	}
	label = codexWindowLabel(label, window.LimitWindowSeconds)
	row := QuotaRow{
		Key:               key,
		Label:             label,
		Scope:             scope,
		Metric:            metric,
		UsedPercent:       floatPtr(window.UsedPercent),
		Allowed:           info.Allowed,
		LimitReached:      info.LimitReached,
		ResetAfterSeconds: intPtr(window.ResetAfterSeconds),
	}
	if window.LimitWindowSeconds != 0 {
		row.Window = &QuotaWindow{Seconds: intPtr(window.LimitWindowSeconds)}
	}
	if window.ResetAt != 0 {
		row.ResetAt = time.Unix(window.ResetAt, 0).UTC().Format(time.RFC3339)
	}
	return append(rows, row)
}

func normalizeGeminiCLIQuotaRows(result GeminiCLIResult) []QuotaRow {
	// Gemini CLI may return both model buckets and Code Assist credits; flatten both for the frontend.
	rows := make([]QuotaRow, 0)
	if result.Quota != nil {
		for _, bucket := range result.Quota.Buckets {
			rows = append(rows, QuotaRow{
				Key:               "bucket." + bucket.ModelID + "." + bucket.TokenType,
				Label:             bucket.ModelID,
				Scope:             "model",
				Metric:            bucket.TokenType,
				Remaining:         floatPtr(bucket.RemainingAmount),
				RemainingFraction: floatPtr(bucket.RemainingFraction),
				ResetAt:           bucket.ResetTime,
			})
		}
	}
	if result.CodeAssist != nil {
		rows = appendGeminiCLICredits(rows, "code_assist.current_tier", result.CodeAssist.CurrentTier)
		rows = appendGeminiCLICredits(rows, "code_assist.paid_tier", result.CodeAssist.PaidTier)
	}
	return rows
}

func appendGeminiCLICredits(rows []QuotaRow, keyPrefix string, tier *GeminiCliUserTier) []QuotaRow {
	if tier == nil {
		return rows
	}
	for _, credit := range tier.AvailableCredits {
		rows = append(rows, QuotaRow{
			Key:       keyPrefix + "." + credit.CreditType,
			Label:     "Code Assist Credit",
			Scope:     "credits",
			Metric:    credit.CreditType,
			Remaining: floatPtr(credit.CreditAmount),
		})
	}
	return rows
}

func normalizeAntigravityQuotaRows(result AntigravityResult) []QuotaRow {
	if result.Quota == nil {
		return nil
	}
	keys := make([]string, 0, len(result.Quota.Models))
	for key := range result.Quota.Models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]QuotaRow, 0, len(keys))
	for _, key := range keys {
		model := result.Quota.Models[key]
		label := model.DisplayName
		if label == "" {
			label = key
		}
		row := QuotaRow{Key: "model." + key, Label: label, Scope: "model", Metric: key}
		if model.QuotaInfo != nil {
			row.Remaining = floatPtr(model.QuotaInfo.Remaining)
			row.RemainingFraction = floatPtr(model.QuotaInfo.RemainingFraction)
			row.ResetAt = model.QuotaInfo.ResetTime
		}
		rows = append(rows, row)
	}
	return rows
}

func normalizeQoderQuotaRows(result QoderResult) []QuotaRow {
	// Official OpenAPI returns one credits bucket; surface used/limit/remaining plus the exceed flag.
	if result.Usage == nil {
		return nil
	}
	usage := result.Usage
	metric := firstNonEmpty(usage.UsageType, "credits")
	row := QuotaRow{
		Key:          "credits",
		Label:        "Credits",
		Scope:        "credits",
		Metric:       metric,
		LimitReached: boolPtr(usage.IsQuotaExceeded),
		Allowed:      boolPtr(!usage.IsQuotaExceeded),
		ResetAt:      qoderExpiresAtRFC3339(usage.ExpiresAt),
	}
	if usage.UserQuota != nil {
		row.Used = floatPtr(usage.UserQuota.Used)
		row.Limit = floatPtr(usage.UserQuota.Total)
		row.Remaining = floatPtr(usage.UserQuota.Remaining)
		if usage.UserQuota.Unit != "" {
			row.Metric = usage.UserQuota.Unit
		}
		if usage.UserQuota.Percentage != 0 {
			row.UsedPercent = floatPtr(usage.UserQuota.Percentage)
		}
	}
	if usage.TotalUsagePercentage != 0 || row.UsedPercent == nil {
		row.UsedPercent = floatPtr(usage.TotalUsagePercentage)
	}
	return []QuotaRow{row}
}

func normalizeCommandCodeQuotaRows(result CommandCodeResult) []QuotaRow {
	if result.Usage == nil {
		return nil
	}
	u := result.Usage
	rows := make([]QuotaRow, 0, 4)

	// Summary / Credits Row
	creditsRow := QuotaRow{
		Key:    "credits",
		Label:  "Credits",
		Scope:  firstNonEmpty(u.PeriodBasis, "billing-period"),
		Metric: "USD",
		Used:   floatPtr(u.TotalCredits),
	}
	if u.TotalCost > 0 && creditsRow.Used == nil {
		creditsRow.Used = floatPtr(u.TotalCost)
	}
	rows = append(rows, creditsRow)

	// Tokens Row
	if u.TotalTokens > 0 || u.TotalTokensIn > 0 || u.TotalTokensOut > 0 {
		tokensRow := QuotaRow{
			Key:    "tokens",
			Label:  "Tokens",
			Scope:  "tokens",
			Metric: "tokens",
			Used:   floatPtr(float64(u.TotalTokens)),
		}
		rows = append(rows, tokensRow)
	}

	// Requests Row
	if u.TotalCount > 0 {
		reqRow := QuotaRow{
			Key:    "requests",
			Label:  "Requests",
			Scope:  "requests",
			Metric: "count",
			Used:   floatPtr(float64(u.TotalCount)),
		}
		if u.SuccessRate > 0 {
			reqRow.UsedPercent = floatPtr(u.SuccessRate)
		}
		rows = append(rows, reqRow)
	}

	return rows
}

func qoderExpiresAtRFC3339(expiresAt int64) string {
	if expiresAt <= 0 {
		return ""
	}
	// Official values are epoch milliseconds; accept seconds for defensive compatibility.
	seconds := expiresAt
	if expiresAt > 1_000_000_000_000 {
		seconds = expiresAt / 1000
	}
	// Ignore far-future sentinels such as year-9999 placeholders.
	const maxReasonableUnix = 4102444800 // 2100-01-01 UTC
	if seconds > maxReasonableUnix {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func normalizeKimiQuotaRows(result KimiResult) []QuotaRow {
	// Kimi summary and limits differ in shape; keep summary first, then expand each limit entry.
	if result.Usage == nil {
		return nil
	}
	rows := make([]QuotaRow, 0, 1+len(result.Usage.Limits))
	if isMeaningfulKimiDetail(result.Usage.Usage) {
		rows = append(rows, kimiDetailQuotaRow("usage", "summary", "Usage", result.Usage.Usage))
	}
	for index, limit := range result.Usage.Limits {
		keyName := limit.Name
		if keyName == "" {
			keyName = fmt.Sprintf("%d", index)
		}
		label := firstNonEmpty(limit.Title, limit.Name, "Limit")
		scope := firstNonEmpty(limit.Scope, "limit")
		row := QuotaRow{
			Key:       "limits." + keyName,
			Label:     label,
			Scope:     scope,
			Metric:    limit.Name,
			Used:      floatPtr(limit.Used),
			Limit:     floatPtr(limit.Limit),
			Remaining: floatPtr(limit.Remaining),
			ResetAt:   firstNonEmpty(limit.ResetAt, resetAtFromKimiDetail(limit.Detail)),
		}
		if limit.Limit > 0 {
			row.UsedPercent = floatPtr(limit.Used / limit.Limit * 100)
		}
		if limit.ResetIn != 0 {
			row.ResetAfterSeconds = intPtr(int64(limit.ResetIn))
		} else if limit.Detail != nil && limit.Detail.ResetIn != 0 {
			row.ResetAfterSeconds = intPtr(int64(limit.Detail.ResetIn))
		}
		row.Window = kimiWindow(limit)
		rows = append(rows, row)
	}
	return rows
}

func kimiDetailQuotaRow(key string, scope string, fallbackLabel string, detail *KimiUsageDetail) QuotaRow {
	row := QuotaRow{
		Key:       key,
		Label:     firstNonEmpty(detail.Title, fallbackLabel),
		Scope:     scope,
		Metric:    detail.Name,
		Used:      floatPtr(detail.Used),
		Limit:     floatPtr(detail.Limit),
		Remaining: floatPtr(detail.Remaining),
		ResetAt:   detail.ResetAt,
	}
	if detail.Limit > 0 {
		row.UsedPercent = floatPtr(detail.Used / detail.Limit * 100)
	}
	if detail.ResetIn != 0 {
		row.ResetAfterSeconds = intPtr(int64(detail.ResetIn))
	}
	return row
}

func isMeaningfulKimiDetail(detail *KimiUsageDetail) bool {
	if detail == nil {
		return false
	}
	return detail.Used != 0 || detail.Limit != 0 || detail.Remaining != 0 || detail.Name != "" || detail.Title != "" || detail.ResetAt != "" || detail.ResetIn != 0 || detail.TTL != 0
}

func resetAtFromKimiDetail(detail *KimiUsageDetail) string {
	if detail == nil {
		return ""
	}
	return detail.ResetAt
}

func kimiWindow(limit KimiLimitItem) *QuotaWindow {
	if limit.Window != nil {
		return &QuotaWindow{Duration: floatPtr(float64(limit.Window.Duration)), Unit: limit.Window.TimeUnit}
	}
	if limit.Duration != 0 || limit.TimeUnit != "" {
		return &QuotaWindow{Duration: floatPtr(float64(limit.Duration)), Unit: limit.TimeUnit}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func floatPtr(value float64) *float64 {
	return &value
}

func intPtr(value int64) *int64 {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
