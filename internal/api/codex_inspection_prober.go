package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexinspection"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/quota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func (p *codexInspectionProber) ProbeCodexAccounts(ctx context.Context, files []codexinspection.AuthFileRecord, settings codexinspection.InspectionSettings) ([]codexinspection.InspectionResultItem, error) {
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

	if response.StatusCode == http.StatusUnauthorized {
		result.Action = codexinspection.ActionDelete
		result.Error = codexInspectionErrorText(response)
		result.ActionReason = "401 response"
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
	if disabled {
		if weeklyUsedPercent != nil && *weeklyUsedPercent < settings.WeeklyUsedPercentThreshold {
			return codexinspection.ActionEnable, fmt.Sprintf("weeklyUsedPercent < %d", settings.WeeklyUsedPercentThreshold)
		}
		if weeklyUsedPercent == nil && fiveHourUsedPercent != nil && *fiveHourUsedPercent < settings.FiveHourUsedPercentThreshold {
			return codexinspection.ActionEnable, fmt.Sprintf("fiveHourUsedPercent < %d", settings.FiveHourUsedPercentThreshold)
		}
		return codexinspection.ActionKeep, "no issue detected"
	}
	if weeklyUsedPercent != nil {
		if *weeklyUsedPercent >= settings.WeeklyUsedPercentThreshold {
			return codexinspection.ActionDisable, fmt.Sprintf("weeklyUsedPercent >= %d", settings.WeeklyUsedPercentThreshold)
		}
		return codexinspection.ActionKeep, "no issue detected"
	}
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
