package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var antigravitySkipDiscoveryModels = map[string]bool{
	"chat_20706":                true,
	"chat_23310":                true,
	"gemini-2.5-flash-thinking": true,
	"gemini-3-pro-low":          true,
	"gemini-2.5-pro":            true,
}

var antigravityUpstreamToAlias = map[string]string{
	"rev19-uic3-1p":              "gemini-2.5-computer-use-preview-10-2025",
	"gemini-3-pro-image":         "gemini-3-pro-image-preview",
	"gemini-3-pro-high":          "gemini-3-pro-preview",
	"gemini-3-flash":             "gemini-3-flash-preview",
	"claude-sonnet-4-5":          "claude-sonnet-4-5",
	"claude-sonnet-4-5-thinking": "claude-sonnet-4-5-thinking",
	"claude-opus-4-5-thinking":   "claude-opus-4-5-thinking",
	"claude-opus-4-6-thinking":   "claude-opus-4-6-thinking",
}

func DefaultAntigravityModels() []string {
	return []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
		"gemini-3-pro-image-preview",
		"gemini-2.5-computer-use-preview-10-2025",
		"claude-sonnet-4-5",
		"claude-sonnet-4-5-thinking",
		"claude-opus-4-5-thinking",
		"claude-opus-4-6-thinking",
		"gpt-oss-120b-medium",
	}
}

type AntigravityCreditStatus struct {
	CreditType              string `json:"credit_type"`
	CreditAmount            string `json:"credit_amount"`
	MinCreditAmountForUsage string `json:"min_credit_amount_for_usage,omitempty"`
}

type AntigravityModelQuota struct {
	Model             string   `json:"model"`
	DisplayName       string   `json:"display_name,omitempty"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	RemainingPercent  *float64 `json:"remaining_percent,omitempty"`
	UsedPercent       *float64 `json:"used_percent,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
}

type AntigravityProbeStatus struct {
	Source  string `json:"source"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type AntigravityUsageResult struct {
	ProjectID       string                    `json:"project_id,omitempty"`
	Email           string                    `json:"email,omitempty"`
	CurrentTier     string                    `json:"current_tier,omitempty"`
	CurrentTierName string                    `json:"current_tier_name,omitempty"`
	PaidTier        string                    `json:"paid_tier,omitempty"`
	PaidTierName    string                    `json:"paid_tier_name,omitempty"`
	Credits         []AntigravityCreditStatus `json:"credits,omitempty"`
	Models          []AntigravityModelQuota   `json:"models,omitempty"`
	Probes          []AntigravityProbeStatus  `json:"probes"`
	FetchedAt       string                    `json:"fetched_at"`
}

func ResolveAntigravityBaseURLs(baseURL string) []string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	candidates := make([]string, 0, 3)
	if trimmed != "" {
		candidates = append(candidates, trimmed)
	}
	for _, fallback := range []string{DefaultAntigravityBaseURL, AntigravityProdBaseURL} {
		if !slices.Contains(candidates, fallback) {
			candidates = append(candidates, fallback)
		}
	}
	return candidates
}

func FetchAntigravityChannelModels(ctx context.Context, baseURL, rawKey, proxyURL string) ([]string, error) {
	accessToken, err := ExtractAntigravityAccessToken(rawKey)
	if err != nil {
		return nil, err
	}
	var projectID string
	if parsedKey, ok := ParseAntigravityOAuthKey(rawKey); ok {
		projectID = parsedKey.ProjectID
	}

	client, err := newAntigravityHTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{}
	if projectID != "" {
		payload["project"] = projectID
	}
	rawBody, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, candidateBase := range ResolveAntigravityBaseURLs(baseURL) {
		endpoint := candidateBase + "/v1internal:fetchAvailableModels"
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		ApplyAntigravityHeaders(req.Header, accessToken, false)

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			lastErr = fmt.Errorf("fetchAvailableModels returned status %d", resp.StatusCode)
			continue
		}

		models, parseErr := parseAntigravityAvailableModelsList(body)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		if len(models) > 0 {
			return models, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return DefaultAntigravityModels(), nil
}

func parseAntigravityAvailableModelsList(body []byte) ([]string, error) {
	var parsed struct {
		Models map[string]any `json:"models"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Models) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(parsed.Models)+len(DefaultAntigravityModels()))
	addModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		seen[id] = struct{}{}
	}

	for modelID := range parsed.Models {
		if alias, ok := antigravityUpstreamToAlias[modelID]; ok {
			addModel(alias)
		}
		if !antigravitySkipDiscoveryModels[modelID] {
			addModel(modelID)
		}
	}
	for _, builtin := range DefaultAntigravityModels() {
		addModel(builtin)
	}

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}

func FetchAntigravityUsage(ctx context.Context, baseURL, rawKey, proxyURL string) (*AntigravityUsageResult, error) {
	accessToken, err := ExtractAntigravityAccessToken(rawKey)
	if err != nil {
		return nil, err
	}

	out := &AntigravityUsageResult{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if parsedKey, ok := ParseAntigravityOAuthKey(rawKey); ok {
		out.ProjectID = parsedKey.ProjectID
		out.Email = parsedKey.Email
		out.PaidTier = parsedKey.PlanTier
	}

	client, err := newAntigravityHTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return nil, err
	}

	// 1. Probe loadCodeAssist for project, tier, and available credits.
	loadAssistURL := AntigravityProdBaseURL + "/v1internal:loadCodeAssist"
	if antigravityLoadCodeAssistOverride != "" {
		loadAssistURL = antigravityLoadCodeAssistOverride
	}
	loadReqBody, _ := common.Marshal(map[string]any{
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	})
	if req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, loadAssistURL, bytes.NewReader(loadReqBody)); reqErr == nil {
		ApplyAntigravityHeaders(req.Header, accessToken, false)
		resp, doErr := client.Do(req)
		if doErr != nil {
			out.Probes = append(out.Probes, AntigravityProbeStatus{
				Source:  "load_code_assist",
				Path:    "/v1internal:loadCodeAssist",
				OK:      false,
				Message: doErr.Error(),
			})
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
			resp.Body.Close()
			ok := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
			probe := AntigravityProbeStatus{
				Source: "load_code_assist",
				Path:   "/v1internal:loadCodeAssist",
				Status: resp.StatusCode,
				OK:     ok,
			}
			if ok {
				applyAntigravityLoadCodeAssistUsage(out, body)
			} else {
				probe.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			out.Probes = append(out.Probes, probe)
		}
	}

	// 2. Probe fetchAvailableModels for per-model quota fractions and reset times.
	modelsPayload := map[string]any{}
	if out.ProjectID != "" {
		modelsPayload["project"] = out.ProjectID
	}
	modelsReqBody, _ := common.Marshal(modelsPayload)
	for _, candidateBase := range ResolveAntigravityBaseURLs(baseURL) {
		endpoint := candidateBase + "/v1internal:fetchAvailableModels"
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(modelsReqBody))
		if reqErr != nil {
			continue
		}
		ApplyAntigravityHeaders(req.Header, accessToken, false)
		resp, doErr := client.Do(req)
		if doErr != nil {
			out.Probes = append(out.Probes, AntigravityProbeStatus{
				Source:  "fetch_available_models",
				Path:    "/v1internal:fetchAvailableModels",
				OK:      false,
				Message: doErr.Error(),
			})
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes))
		resp.Body.Close()
		ok := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
		probe := AntigravityProbeStatus{
			Source: "fetch_available_models",
			Path:   "/v1internal:fetchAvailableModels",
			Status: resp.StatusCode,
			OK:     ok,
		}
		if ok {
			applyAntigravityModelsQuota(out, body)
			out.Probes = append(out.Probes, probe)
			break
		}
		probe.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		out.Probes = append(out.Probes, probe)
	}

	return out, nil
}

func applyAntigravityLoadCodeAssistUsage(out *AntigravityUsageResult, body []byte) {
	var parsed map[string]any
	if err := common.Unmarshal(body, &parsed); err != nil {
		return
	}
	if projID := extractAntigravityProjectID(parsed); projID != "" {
		out.ProjectID = projID
	}
	if currentTier, ok := parsed["currentTier"].(map[string]any); ok {
		if id, ok := currentTier["id"].(string); ok && strings.TrimSpace(id) != "" {
			out.CurrentTier = strings.TrimSpace(id)
		}
		if name, ok := currentTier["name"].(string); ok && strings.TrimSpace(name) != "" {
			out.CurrentTierName = strings.TrimSpace(name)
		}
	}
	if paidTier, ok := parsed["paidTier"].(map[string]any); ok {
		if id, ok := paidTier["id"].(string); ok && strings.TrimSpace(id) != "" {
			out.PaidTier = strings.TrimSpace(id)
		}
		if name, ok := paidTier["name"].(string); ok && strings.TrimSpace(name) != "" {
			out.PaidTierName = strings.TrimSpace(name)
		}
		if credits, ok := paidTier["availableCredits"].([]any); ok {
			for _, item := range credits {
				cm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				creditType, _ := cm["creditType"].(string)
				creditAmount, _ := cm["creditAmount"].(string)
				minCredit, _ := cm["minimumCreditAmountForUsage"].(string)
				if strings.TrimSpace(creditType) == "" && strings.TrimSpace(creditAmount) == "" {
					continue
				}
				out.Credits = append(out.Credits, AntigravityCreditStatus{
					CreditType:              strings.TrimSpace(creditType),
					CreditAmount:            strings.TrimSpace(creditAmount),
					MinCreditAmountForUsage: strings.TrimSpace(minCredit),
				})
			}
		}
	}
}

func applyAntigravityModelsQuota(out *AntigravityUsageResult, body []byte) {
	var parsed struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			QuotaInfo   *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil || len(parsed.Models) == 0 {
		return
	}

	quotas := make([]AntigravityModelQuota, 0, len(parsed.Models))
	for modelID, info := range parsed.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		entry := AntigravityModelQuota{
			Model:       modelID,
			DisplayName: strings.TrimSpace(info.DisplayName),
		}
		if info.QuotaInfo != nil {
			entry.ResetTime = strings.TrimSpace(info.QuotaInfo.ResetTime)
			if info.QuotaInfo.RemainingFraction != nil {
				frac := *info.QuotaInfo.RemainingFraction
				if frac < 0 {
					frac = 0
				} else if frac > 1 {
					frac = 1
				}
				remPct := math.Round(frac*1000) / 10
				usedPct := math.Round((1-frac)*1000) / 10
				entry.RemainingFraction = &frac
				entry.RemainingPercent = &remPct
				entry.UsedPercent = &usedPct
			}
		}
		quotas = append(quotas, entry)
	}

	slices.SortFunc(quotas, func(a, b AntigravityModelQuota) int {
		return strings.Compare(a.Model, b.Model)
	})
	out.Models = quotas
}
