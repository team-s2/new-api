package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
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

type AntigravityQuotaGroup struct {
	DisplayName string                   `json:"display_name,omitempty"`
	Description string                   `json:"description,omitempty"`
	Buckets     []AntigravityQuotaBucket `json:"buckets,omitempty"`
}

type AntigravityQuotaBucket struct {
	BucketID          string   `json:"bucket_id,omitempty"`
	DisplayName       string   `json:"display_name,omitempty"`
	Window            string   `json:"window,omitempty"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
	Description       string   `json:"description,omitempty"`
	Disabled          bool     `json:"disabled,omitempty"`
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
	QuotaGroups     []AntigravityQuotaGroup   `json:"quota_groups,omitempty"`
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

	var lastErr error
	for _, candidateBase := range ResolveAntigravityBaseURLs(baseURL) {
		status, body, postErr := postAntigravityCloudCode(ctx, client, candidateBase+"/v1internal:fetchAvailableModels", accessToken, payload)
		if postErr != nil {
			lastErr = postErr
			continue
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			lastErr = fmt.Errorf("fetchAvailableModels returned status %d", status)
			continue
		}

		models, parseErr := parseAntigravityAvailableModelsList(body)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		return models, nil
	}

	return nil, lastErr
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

	seen := make(map[string]struct{}, len(parsed.Models))
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

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}

func FetchAntigravityUsage(ctx context.Context, baseURL, rawKey, proxyURL string) (*AntigravityUsageResult, error) {
	return fetchAntigravityUsage(ctx, baseURL, rawKey, proxyURL, false)
}

// FetchAntigravityQuotaSummary retrieves the native grouped quota windows
// without waiting for the independent model catalog and account metadata probes.
func FetchAntigravityQuotaSummary(ctx context.Context, baseURL, rawKey, proxyURL string) (*AntigravityUsageResult, error) {
	return fetchAntigravityUsage(ctx, baseURL, rawKey, proxyURL, true)
}

func fetchAntigravityUsage(ctx context.Context, baseURL, rawKey, proxyURL string, summaryOnly bool) (*AntigravityUsageResult, error) {
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

	if summaryOnly {
		// The native groups are authoritative. Never fill missing groups or windows
		// from a different endpoint or from the legacy per-model fractions.
		summaryPayload := map[string]any{}
		if out.ProjectID != "" {
			summaryPayload["project"] = out.ProjectID
		}
		summaryCtx, cancelSummary := context.WithTimeout(ctx, 15*time.Second)
		for _, candidateBase := range ResolveAntigravityBaseURLs(baseURL) {
			if summaryCtx.Err() != nil {
				break
			}
			probe, groups := probeAntigravityQuotaSummary(summaryCtx, client, candidateBase+"/v1internal:retrieveUserQuotaSummary", accessToken, summaryPayload)
			out.Probes = append(out.Probes, probe)
			if probe.OK {
				out.QuotaGroups = groups
				break
			}
		}
		cancelSummary()
		return out, nil
	}

	// Probe loadCodeAssist for project, tier, and available credits.
	probe, body := probeAntigravityUsageEndpoint(ctx, client, "load_code_assist", antigravityLoadCodeAssistEndpoint(), "/v1internal:loadCodeAssist", accessToken, map[string]any{
		"metadata": antigravityIDEMetadata,
	})
	out.Probes = append(out.Probes, probe)
	if probe.OK {
		applyAntigravityLoadCodeAssistUsage(out, body)
	}

	// Probe fetchAvailableModels for per-model quota fractions and reset times.
	modelsPayload := map[string]any{}
	if out.ProjectID != "" {
		modelsPayload["project"] = out.ProjectID
	}
	for _, candidateBase := range ResolveAntigravityBaseURLs(baseURL) {
		probe, body := probeAntigravityUsageEndpoint(ctx, client, "fetch_available_models", candidateBase+"/v1internal:fetchAvailableModels", "/v1internal:fetchAvailableModels", accessToken, modelsPayload)
		out.Probes = append(out.Probes, probe)
		if probe.OK {
			applyAntigravityModelsQuota(out, body)
			break
		}
	}

	return out, nil
}

func probeAntigravityQuotaSummary(ctx context.Context, client *http.Client, endpoint, accessToken string, payload any) (AntigravityProbeStatus, []AntigravityQuotaGroup) {
	probe := AntigravityProbeStatus{Source: "retrieve_user_quota_summary", Path: "/v1internal:retrieveUserQuotaSummary"}
	rawBody, err := common.Marshal(payload)
	if err != nil {
		probe.Message = "Failed to encode quota summary request"
		return probe, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		probe.Message = "Invalid quota summary endpoint"
		return probe, nil
	}
	ApplyAntigravityHeaders(req.Header, accessToken, false)
	// The grouped quota API uses the native CLI identity, independently of the
	// hub identity used by relay and legacy probes. Keep the operator override.
	userAgent := strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_AGENT"))
	if userAgent == "" {
		userAgent = "antigravity/cli/1.5.5"
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		probe.Message = "Quota summary request failed"
		return probe, nil
	}
	defer resp.Body.Close()
	probe.Status = resp.StatusCode
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		probe.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return probe, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, antigravityOAuthMaxResponseBytes+1))
	if err != nil {
		probe.Message = "Failed to read quota summary response"
		return probe, nil
	}
	if len(body) > antigravityOAuthMaxResponseBytes {
		probe.Message = "Quota summary response is too large"
		return probe, nil
	}
	var parsed *struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				DisplayName       string   `json:"displayName"`
				Window            string   `json:"window"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				Description       string   `json:"description"`
				Disabled          bool     `json:"disabled"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil || parsed == nil {
		probe.Message = "Invalid quota summary response"
		return probe, nil
	}
	groups := make([]AntigravityQuotaGroup, 0, len(parsed.Groups))
	for _, group := range parsed.Groups {
		entry := AntigravityQuotaGroup{
			DisplayName: group.DisplayName,
			Description: group.Description,
			Buckets:     make([]AntigravityQuotaBucket, 0, len(group.Buckets)),
		}
		for _, bucket := range group.Buckets {
			entry.Buckets = append(entry.Buckets, AntigravityQuotaBucket{
				BucketID:          bucket.BucketID,
				DisplayName:       bucket.DisplayName,
				Window:            bucket.Window,
				RemainingFraction: bucket.RemainingFraction,
				ResetTime:         bucket.ResetTime,
				Description:       bucket.Description,
				Disabled:          bucket.Disabled,
			})
		}
		groups = append(groups, entry)
	}
	probe.OK = true
	return probe, groups
}

// probeAntigravityUsageEndpoint calls one usage source and reports its outcome
// for the usage dialog; the body is only meaningful when the probe is OK.
func probeAntigravityUsageEndpoint(ctx context.Context, client *http.Client, source, endpoint, path, accessToken string, payload any) (AntigravityProbeStatus, []byte) {
	probe := AntigravityProbeStatus{Source: source, Path: path}
	status, body, err := postAntigravityCloudCode(ctx, client, endpoint, accessToken, payload)
	probe.Status = status
	switch {
	case err != nil:
		probe.Message = err.Error()
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		probe.Message = fmt.Sprintf("HTTP %d", status)
	default:
		probe.OK = true
	}
	return probe, body
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
