package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const defaultCodexInspectionTimeout = 60 * time.Second
const codexInspectionFiveHourSeconds = 18000
const codexInspectionWeekSeconds = 604800

type codexInspectionProber struct {
	cfg     *config.Config
	manager *coreauth.Manager
}

func newCodexInspectionProber(cfg *config.Config, manager *coreauth.Manager) codexinspection.Prober {
	return &codexInspectionProber{cfg: cfg, manager: manager}
}

func (p *codexInspectionProber) ProbeAccounts(ctx context.Context, files []codexinspection.AuthFileRecord, settings codexinspection.InspectionSettings) ([]codexinspection.InspectionResultItem, error) {
	if len(files) == 0 {
		return []codexinspection.InspectionResultItem{}, nil
	}
	if settings.SampleSize > 0 && settings.SampleSize < len(files) {
		files = files[:settings.SampleSize]
	}

	template := quota.DefaultProviderConfigs().Codex
	workers := settings.Workers
	if workers <= 0 {
		workers = 1
	}
	retries := settings.Retries
	if retries < 0 {
		retries = 0
	}

	results := make([]codexinspection.InspectionResultItem, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = p.inspectFile(ctx, files[idx], settings, template, retries)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results, nil
}

func (p *codexInspectionProber) inspectFile(ctx context.Context, file codexinspection.AuthFileRecord, settings codexinspection.InspectionSettings, template quota.APICallConfig, retries int) codexinspection.InspectionResultItem {
	result := codexinspection.InspectionResultItem{
		FileName:     file.FileName,
		DisplayName:  file.DisplayName,
		Provider:     file.Provider,
		AuthIndex:    file.AuthIndex,
		AccountID:    file.AccountID,
		Disabled:     file.Disabled,
		Action:       codexinspection.ActionKeep,
		ActionReason: "no issue detected",
		Executable:   true,
	}

	if !strings.EqualFold(strings.TrimSpace(file.Provider), "codex") {
		return p.inspectProviderAuth(ctx, file, settings, result)
	}

	if strings.TrimSpace(file.AuthIndex) == "" {
		result.Error = "auth index missing"
		result.ActionReason = "auth index missing"
		return result
	}

	var response *apicall.Response
	var callErr error
	for attempt := 0; attempt <= retries; attempt++ {
		response, callErr = p.callCodexUsage(ctx, file, settings, template)
		if callErr == nil {
			break
		}
	}
	if response != nil {
		result.StatusCode = response.StatusCode
	}
	if callErr != nil {
		result.Error = callErr.Error()
		result.ActionReason = callErr.Error()
		return result
	}
	if response == nil {
		result.Error = "empty response"
		result.ActionReason = "empty response"
		return result
	}

	if response.StatusCode >= http.StatusBadRequest {
		result.Error = codexInspectionErrorText(response)
		if action, ok := inspectionActionForStatusCode(settings, file.Provider, response.StatusCode); ok {
			result.Action = action
			result.ActionReason = fmt.Sprintf("%d response", response.StatusCode)
			return result
		}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusPaymentRequired {
		result.Action = codexinspection.ActionDelete
		if response.StatusCode == http.StatusUnauthorized {
			result.ActionReason = "401 response"
		} else {
			result.ActionReason = "402 response"
		}
		return result
	}
	if response.StatusCode >= http.StatusBadRequest {
		result.Error = codexInspectionErrorText(response)
		result.ActionReason = result.Error
		return result
	}

	payload, err := quota.ParseCodexUsagePayload(response)
	if err != nil {
		result.Error = err.Error()
		result.ActionReason = err.Error()
		return result
	}
	fiveHourUsedPercent, weeklyUsedPercent, usedPercent, ok := codexInspectionUsagePercents(payload)
	if fiveHourUsedPercent != nil {
		result.FiveHourUsedPercent = fiveHourUsedPercent
	}
	if weeklyUsedPercent != nil {
		result.WeeklyUsedPercent = weeklyUsedPercent
	}
	if ok {
		result.UsedPercent = usedPercent
	}
	result.Action, result.ActionReason = codexInspectionActionForUsage(file.Disabled, fiveHourUsedPercent, weeklyUsedPercent, settings)
	return result
}

func (p *codexInspectionProber) inspectProviderAuth(ctx context.Context, file codexinspection.AuthFileRecord, settings codexinspection.InspectionSettings, result codexinspection.InspectionResultItem) codexinspection.InspectionResultItem {
	auth, err := p.authForFile(file)
	if err != nil {
		result.Error = err.Error()
		result.Action = codexinspection.ActionFailed
		result.ActionReason = err.Error()
		return result
	}

	executor, ok := p.manager.Executor(auth.Provider)
	if !ok {
		result.Error = fmt.Sprintf("provider executor not found: %s", auth.Provider)
		result.Action = codexinspection.ActionFailed
		result.ActionReason = result.Error
		return result
	}

	timeout := defaultCodexInspectionTimeout
	if settings.TimeoutSeconds > 0 {
		timeout = time.Duration(settings.TimeoutSeconds) * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if prober, okProbe := executor.(coreauth.AuthProber); okProbe {
		if probeErr := prober.ProbeAuth(probeCtx, auth.Clone()); probeErr != nil {
			if strings.EqualFold(strings.TrimSpace(file.Provider), "xai") {
				return inspectionXAIProbeErrorResult(result, settings, file.Provider, probeErr)
			}
			return inspectionProviderErrorResult(result, settings, file.Provider, probeErr)
		}
		if strings.EqualFold(strings.TrimSpace(file.Provider), "xai") {
			result.ActionReason = codexinspection.XAIProbeSucceededReason
			if result.Disabled {
				result.ActionReason = codexinspection.XAIProbeSucceededDisabledReason
				result.Executable = false
			}
		}
		return result
	}

	refreshed, refreshErr := executor.Refresh(probeCtx, auth.Clone())
	if refreshErr != nil {
		return inspectionProviderErrorResult(result, settings, file.Provider, refreshErr)
	}
	updates, deletes := inspectionMetadataChanges(auth, refreshed)
	if len(updates) == 0 && len(deletes) == 0 {
		result.Error = "provider inspection unsupported: refresh did not validate credentials"
		result.Action = codexinspection.ActionFailed
		result.ActionReason = result.Error
		return result
	}
	if _, err = p.manager.MergeMetadata(ctx, auth.ID, updates, deletes); err != nil {
		result.Error = fmt.Sprintf("persist refreshed auth: %v", err)
		result.Action = codexinspection.ActionFailed
		result.ActionReason = result.Error
	}
	return result
}

func inspectionProviderErrorResult(result codexinspection.InspectionResultItem, settings codexinspection.InspectionSettings, provider string, probeErr error) codexinspection.InspectionResultItem {
	result.StatusCode = inspectionStatusCode(probeErr)
	result.Error = probeErr.Error()
	result.ActionReason = result.Error
	if action, ok := inspectionActionForStatusCode(settings, provider, result.StatusCode); ok {
		result.Action = action
		result.ActionReason = fmt.Sprintf("%d response", result.StatusCode)
		return result
	}
	if inspectionNeedsReauth(probeErr, result.StatusCode) {
		result.Action = codexinspection.ActionReauth
		if result.StatusCode > 0 {
			result.ActionReason = fmt.Sprintf("%d response", result.StatusCode)
		}
	} else {
		result.Action = codexinspection.ActionFailed
	}
	return result
}

func inspectionXAIProbeErrorResult(result codexinspection.InspectionResultItem, settings codexinspection.InspectionSettings, provider string, probeErr error) codexinspection.InspectionResultItem {
	result.StatusCode = inspectionStatusCode(probeErr)
	result.Error = probeErr.Error()
	result.ActionReason = result.Error
	if action, ok := inspectionActionForStatusCode(settings, provider, result.StatusCode); ok {
		result.Action = action
		result.ActionReason = fmt.Sprintf("%d response", result.StatusCode)
		return result
	}

	message := strings.ToLower(probeErr.Error())
	switch {
	case inspectionXAIFreeUsageExhausted(message):
		result.Action = codexinspection.ActionDisable
		result.ActionReason = "xAI free usage exhausted"
	case inspectionXAISpendingLimitReached(message):
		result.Action = codexinspection.ActionDisable
		result.ActionReason = "xAI spending limit reached"
	case result.StatusCode == http.StatusUnauthorized || inspectionXAIAuthInvalid(message):
		result.Action = codexinspection.ActionReauth
		result.ActionReason = "xAI authentication invalid"
	case inspectionXAIEntitlementDenied(message):
		result.Action = codexinspection.ActionDisable
		result.ActionReason = "xAI chat entitlement denied"
	case result.StatusCode == http.StatusTooManyRequests:
		result.Action = codexinspection.ActionFailed
		result.ActionReason = "xAI temporarily rate limited"
	case result.StatusCode == http.StatusNotFound:
		result.Action = codexinspection.ActionFailed
		result.ActionReason = "xAI probe model unavailable"
	case result.StatusCode == http.StatusPaymentRequired || result.StatusCode == http.StatusForbidden:
		result.Action = codexinspection.ActionFailed
		result.ActionReason = fmt.Sprintf("xAI permission or quota status requires review (HTTP %d)", result.StatusCode)
	default:
		result.Action = codexinspection.ActionFailed
		result.ActionReason = "xAI probe failed"
	}
	if result.Disabled && result.Action == codexinspection.ActionDisable {
		result.Action = codexinspection.ActionKeep
		result.ActionReason += "; account already disabled"
		result.Executable = false
	}
	return result
}

func inspectionXAIFreeUsageExhausted(message string) bool {
	return strings.Contains(message, "free-usage-exhausted") ||
		strings.Contains(message, "used all the included free usage") ||
		strings.Contains(message, "included free usage has been exhausted")
}

func inspectionXAISpendingLimitReached(message string) bool {
	for _, marker := range []string{
		"personal-team-blocked:spending-limit",
		"spending-limit",
		"run out of credits",
		"used all available credits",
		"monthly spending limit",
		"purchase more credits",
		"add credits",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func inspectionXAIAuthInvalid(message string) bool {
	for _, marker := range []string{
		"invalid_grant",
		"invalid_refresh_token",
		"token_invalidated",
		"token_revoked",
		"refresh_token_reused",
		"bad-credentials",
		"invalid or expired credentials",
		"authentication token has been invalidated",
		"token has been invalidated",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func inspectionXAIEntitlementDenied(message string) bool {
	for _, marker := range []string{
		"permission-denied",
		"chat endpoint is denied",
		"access to the chat endpoint is denied",
		"need a grok subscription",
		"no active grok subscription",
		"do not have an active grok subscription",
		"not authorized for xai api access",
		"not entitled",
		"subscription required",
		"deactivated",
		"suspended",
		"banned",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func inspectionMetadataChanges(original, refreshed *coreauth.Auth) (map[string]any, []string) {
	if refreshed == nil {
		return nil, nil
	}
	updates := make(map[string]any)
	for key, value := range refreshed.Metadata {
		originalValue, ok := original.Metadata[key]
		if !ok || !reflect.DeepEqual(originalValue, value) {
			updates[key] = value
		}
	}
	deletes := make([]string, 0)
	for key := range original.Metadata {
		if _, ok := refreshed.Metadata[key]; !ok {
			deletes = append(deletes, key)
		}
	}
	return updates, deletes
}

func (p *codexInspectionProber) authForFile(file codexinspection.AuthFileRecord) (*coreauth.Auth, error) {
	if p == nil || p.manager == nil {
		return nil, fmt.Errorf("auth manager unavailable")
	}
	if authID := strings.TrimSpace(file.AuthID); authID != "" {
		if auth, ok := p.manager.GetByID(authID); ok {
			return auth, nil
		}
	}
	return p.authByIndex(file.AuthIndex)
}

func inspectionStatusCode(err error) int {
	if err == nil {
		return 0
	}
	var statusErr cliproxyexecutor.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode()
	}
	return 0
}

func inspectionActionForStatusCode(settings codexinspection.InspectionSettings, provider string, statusCode int) (codexinspection.Action, bool) {
	if statusCode < http.StatusBadRequest || statusCode > 599 {
		return "", false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	actions := settings.StatusCodeActions[provider]
	if actions == nil {
		for configuredProvider, configuredActions := range settings.StatusCodeActions {
			if strings.EqualFold(strings.TrimSpace(configuredProvider), provider) {
				actions = configuredActions
				break
			}
		}
	}
	action, ok := actions[statusCode]
	if !ok {
		return "", false
	}
	switch action {
	case codexinspection.ActionKeep, codexinspection.ActionDelete, codexinspection.ActionDisable, codexinspection.ActionEnable, codexinspection.ActionReauth, codexinspection.ActionFailed:
		return action, true
	default:
		return "", false
	}
}

func inspectionNeedsReauth(err error, statusCode int) bool {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusPaymentRequired || statusCode == http.StatusForbidden {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"unauthorized", "invalid_grant", "invalid token", "token expired", "refresh token missing"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (p *codexInspectionProber) callCodexUsage(ctx context.Context, file codexinspection.AuthFileRecord, settings codexinspection.InspectionSettings, template quota.APICallConfig) (*apicall.Response, error) {
	auth, err := p.authByIndex(file.AuthIndex)
	if err != nil {
		return nil, err
	}
	headers := maps.Clone(template.Headers)
	token := codexInspectionTokenForAuth(auth)
	if token == "" {
		return nil, fmt.Errorf("auth token not found")
	}
	for key, value := range headers {
		headers[key] = strings.ReplaceAll(value, "$TOKEN$", token)
	}
	if accountID := strings.TrimSpace(file.AccountID); accountID != "" {
		headers["Chatgpt-Account-Id"] = accountID
	}

	timeout := defaultCodexInspectionTimeout
	if settings.TimeoutSeconds > 0 {
		timeout = time.Duration(settings.TimeoutSeconds) * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, template.Method, template.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: p.transportForAuth(auth),
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Error("close codex inspection response body")
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	result := &apicall.Response{
		StatusCode: resp.StatusCode,
		BodyText:   strings.TrimSpace(string(body)),
	}
	if json.Valid(body) {
		result.Body = append([]byte(nil), body...)
	}
	return result, nil
}

func (p *codexInspectionProber) authByIndex(authIndex string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth index missing")
	}
	if p == nil || p.manager == nil {
		return nil, fmt.Errorf("auth manager unavailable")
	}
	for _, item := range p.manager.List() {
		if item == nil {
			continue
		}
		item.EnsureIndex()
		if item.Index == authIndex {
			return item, nil
		}
	}
	return nil, fmt.Errorf("auth not found: %s", authIndex)
}

func (p *codexInspectionProber) transportForAuth(auth *coreauth.Auth) http.RoundTripper {
	proxyCandidates := make([]string, 0, 3)
	if auth != nil {
		if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
		if proxyStr := strings.TrimSpace(p.proxyURLFromCodexKey(auth)); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}
	if p != nil && p.cfg != nil {
		if proxyStr := strings.TrimSpace(p.cfg.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}
	for _, proxyStr := range proxyCandidates {
		transport, _, err := proxyutil.BuildHTTPTransport(proxyStr)
		if err == nil {
			return transport
		}
		log.WithError(err).Debug("build codex inspection proxy transport failed")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		return &http.Transport{Proxy: nil}
	}
	clone := transport.Clone()
	clone.Proxy = nil
	return clone
}

func (p *codexInspectionProber) proxyURLFromCodexKey(auth *coreauth.Auth) string {
	if p == nil || p.cfg == nil || auth == nil {
		return ""
	}
	kind, account := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return ""
	}
	attrKey := strings.TrimSpace(account)
	attrBase := ""
	if auth.Attributes != nil {
		if attrKey == "" {
			attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		}
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range p.cfg.CodexKey {
		entry := p.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" && strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
			return strings.TrimSpace(entry.ProxyURL)
		}
	}
	for i := range p.cfg.CodexKey {
		entry := p.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if attrBase == "" || cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return strings.TrimSpace(entry.ProxyURL)
			}
		}
	}
	if attrKey == "" && attrBase != "" {
		for i := range p.cfg.CodexKey {
			entry := p.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.BaseURL), attrBase) {
				return strings.TrimSpace(entry.ProxyURL)
			}
		}
	}
	return ""
}

func codexInspectionTokenForAuth(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if token := codexInspectionTokenFromMetadata(auth.Metadata); token != "" {
		return token
	}
	if auth.Attributes != nil {
		if token := strings.TrimSpace(auth.Attributes["api_key"]); token != "" {
			return token
		}
	}
	return ""
}

func codexInspectionTokenFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{"accessToken", "access_token", "token", "id_token", "cookie"} {
		if value, ok := metadata[key]; ok {
			switch typed := value.(type) {
			case string:
				if token := strings.TrimSpace(typed); token != "" {
					return token
				}
			case map[string]any:
				for _, nestedKey := range []string{"access_token", "accessToken"} {
					if nested, okNested := typed[nestedKey].(string); okNested {
						if token := strings.TrimSpace(nested); token != "" {
							return token
						}
					}
				}
			case map[string]string:
				for _, nestedKey := range []string{"access_token", "accessToken"} {
					if token := strings.TrimSpace(typed[nestedKey]); token != "" {
						return token
					}
				}
			}
		}
	}
	return ""
}

func codexInspectionActionForUsage(disabled bool, fiveHourUsedPercent *int, weeklyUsedPercent *int, settings codexinspection.InspectionSettings) (codexinspection.Action, string) {
	// Weekly remaining below threshold → suggest delete (highest priority).
	if weeklyUsedPercent != nil && *weeklyUsedPercent >= settings.WeeklyUsedPercentThreshold {
		return codexinspection.ActionDelete, fmt.Sprintf("weeklyUsedPercent >= %d", settings.WeeklyUsedPercentThreshold)
	}

	// Enable only when disabled and BOTH 5h/weekly remaining are above thresholds.
	if disabled {
		if fiveHourUsedPercent != nil &&
			weeklyUsedPercent != nil &&
			*fiveHourUsedPercent < settings.FiveHourUsedPercentThreshold &&
			*weeklyUsedPercent < settings.WeeklyUsedPercentThreshold {
			return codexinspection.ActionEnable, fmt.Sprintf(
				"fiveHourUsedPercent < %d && weeklyUsedPercent < %d",
				settings.FiveHourUsedPercentThreshold,
				settings.WeeklyUsedPercentThreshold,
			)
		}
		return codexinspection.ActionKeep, "no issue detected"
	}

	// 5h remaining below threshold → suggest disable.
	if fiveHourUsedPercent != nil && *fiveHourUsedPercent >= settings.FiveHourUsedPercentThreshold {
		return codexinspection.ActionDisable, fmt.Sprintf("fiveHourUsedPercent >= %d", settings.FiveHourUsedPercentThreshold)
	}
	return codexinspection.ActionKeep, "no issue detected"
}

func codexInspectionUsagePercents(payload *quota.CodexUsagePayload) (*int, *int, *int, bool) {
	if payload == nil || payload.RateLimit == nil {
		return nil, nil, nil, false
	}

	primaryWindow := payload.RateLimit.PrimaryWindow
	secondaryWindow := payload.RateLimit.SecondaryWindow
	var fiveHourUsedPercent *int
	var weeklyUsedPercent *int

	for _, window := range []*quota.CodexUsageWindow{primaryWindow, secondaryWindow} {
		if window == nil {
			continue
		}
		usedPercent := int(window.UsedPercent + 0.5)
		switch window.LimitWindowSeconds {
		case codexInspectionFiveHourSeconds:
			if fiveHourUsedPercent == nil {
				fiveHourUsedPercent = &usedPercent
			}
		case codexInspectionWeekSeconds:
			if weeklyUsedPercent == nil {
				weeklyUsedPercent = &usedPercent
			}
		}
	}

	if fiveHourUsedPercent == nil && primaryWindow != nil {
		usedPercent := int(primaryWindow.UsedPercent + 0.5)
		fiveHourUsedPercent = &usedPercent
	}
	if weeklyUsedPercent == nil && secondaryWindow != nil {
		usedPercent := int(secondaryWindow.UsedPercent + 0.5)
		weeklyUsedPercent = &usedPercent
	}

	if weeklyUsedPercent != nil {
		return fiveHourUsedPercent, weeklyUsedPercent, weeklyUsedPercent, true
	}
	if fiveHourUsedPercent != nil {
		return fiveHourUsedPercent, weeklyUsedPercent, fiveHourUsedPercent, true
	}
	return fiveHourUsedPercent, weeklyUsedPercent, nil, false
}

func codexInspectionErrorText(response *apicall.Response) string {
	if response == nil {
		return ""
	}
	if text := strings.TrimSpace(response.BodyText); text != "" {
		return text
	}
	if len(response.Body) > 0 {
		return strings.TrimSpace(string(response.Body))
	}
	return fmt.Sprintf("HTTP %d", response.StatusCode)
}
