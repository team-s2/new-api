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
	// GroupKey is the stable semantic bucket this group belongs to: the upstream
	// display name changes across plans and client versions, while the key lets
	// the console and the QQ renderer keep Gemini, Claude+GPT and the separate
	// Gemini 4 Argon allowance apart.
	GroupKey    string                   `json:"group_key,omitempty"`
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

	// The native groups are the authoritative quota source: they already separate
	// the Gemini pool (Gemini 4 Hydrogen and the Flash models share it), the
	// Claude/GPT pool and the standalone Argon allowance, each with its own
	// windows. Both the console and the QQ card read them from here.
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
	if summaryOnly {
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
	return probe, normalizeAntigravityGroupKeys(groups)
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

// Antigravity reports quota as display-named groups whose names change with the
// plan and the client version ("Gemini Models", "Claude and GPT models", and,
// since the Gemini 4 Argon rollout, a third "Gemini 4 Argon" group). The console
// and the QQ renderer need a stable identity per allowance, so every group is
// mapped onto one of these keys.
const (
	antigravityGroupGemini = "antigravity-gemini-models"
	antigravityGroupClaude = "antigravity-claude-and-gpt-models"
	// antigravityGroupArgon is the standalone weekly Gemini 4 Argon allowance.
	// It must never be folded into the Gemini group: Argon has its own window and
	// drains independently.
	antigravityGroupArgon = "antigravity-gemini-4-argon"
	// antigravityArgonWeeklyBucketID identifies the Argon allowance even when the
	// upstream renames the group.
	antigravityArgonWeeklyBucketID = "gemini-argon-wtu-weekly"
	// antigravityArgonModelPrefix marks the per-model Argon entries, which carry
	// the standalone allowance rather than the shared Gemini pool.
	antigravityArgonModelPrefix  = "gemini-4-argon"
	antigravityGeminiAPIProvider = "API_PROVIDER_GOOGLE_GEMINI"
)

// antigravityGroupKeyFor maps a group display name to its stable key. The exact
// "Gemini 4 Argon" name is matched before the generic Gemini test so the Argon
// allowance keeps its own card.
func antigravityGroupKeyFor(displayName string) string {
	words := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(displayName)), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	if strings.Join(words, " ") == "gemini 4 argon" || strings.Join(words, " ") == "gemini argon" {
		return antigravityGroupArgon
	}

	hasGemini, hasClaude, hasGPT := false, false, false
	for _, word := range words {
		switch word {
		case "gemini":
			hasGemini = true
		case "claude":
			hasClaude = true
		case "gpt":
			hasGPT = true
		}
	}
	switch {
	case hasGemini && !hasClaude && !hasGPT:
		return antigravityGroupGemini
	case hasClaude && !hasGemini:
		return antigravityGroupClaude
	case hasGPT && !hasGemini:
		return antigravityGroupClaude
	default:
		return ""
	}
}

// antigravityGroupKeyForBuckets identifies a group's allowance. The bucket id is
// checked first because it is harder for the upstream to rename than the display
// name, and Argon must stay separate even if a plan relabels its group.
func antigravityGroupKeyForBuckets(group AntigravityQuotaGroup) string {
	for _, bucket := range group.Buckets {
		if strings.EqualFold(strings.TrimSpace(bucket.BucketID), antigravityArgonWeeklyBucketID) {
			return antigravityGroupArgon
		}
	}
	return antigravityGroupKeyFor(group.DisplayName)
}

// normalizeAntigravityGroupKeys stamps the semantic key on every group. Groups
// that cannot be identified keep an empty key and are rendered as reported.
func normalizeAntigravityGroupKeys(groups []AntigravityQuotaGroup) []AntigravityQuotaGroup {
	for i := range groups {
		groups[i].GroupKey = antigravityGroupKeyForBuckets(groups[i])
	}
	return groups
}

// antigravityModelEntry is one per-model quota row from either upstream shape,
// before it is pinned into the display list and aggregated into the Gemini group.
type antigravityModelEntry struct {
	id        string
	display   string
	provider  string
	remaining *float64
	resetTime string
}

func applyAntigravityModelsQuota(out *AntigravityUsageResult, body []byte) {
	// retrieveUserQuota answers with a bucket array instead of the fetchAvailableModels
	// map; both carry the same per-model quota fields.
	var parsed struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			APIProvider string `json:"apiProvider"`
			QuotaInfo   *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
		Buckets []struct {
			ModelID           string   `json:"modelId"`
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"buckets"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil {
		return
	}

	entries := make([]antigravityModelEntry, 0, len(parsed.Models)+len(parsed.Buckets))
	for modelID, info := range parsed.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		entry := antigravityModelEntry{
			id:       modelID,
			display:  strings.TrimSpace(info.DisplayName),
			provider: strings.TrimSpace(info.APIProvider),
		}
		if info.QuotaInfo != nil {
			entry.resetTime = strings.TrimSpace(info.QuotaInfo.ResetTime)
			entry.remaining = info.QuotaInfo.RemainingFraction
		}
		// fetchAvailableModels omits remainingFraction for an exhausted model while
		// retrieveUserQuota reports an explicit 0; treat a reset time without a
		// fraction as exhausted so a drained allowance stays visible.
		if entry.remaining == nil && entry.resetTime != "" {
			zero := 0.0
			entry.remaining = &zero
		}
		entries = append(entries, entry)
	}
	for _, bucket := range parsed.Buckets {
		modelID := strings.TrimSpace(bucket.ModelID)
		if modelID == "" {
			continue
		}
		entries = append(entries, antigravityModelEntry{
			id:        modelID,
			display:   modelID,
			provider:  antigravityAPIProviderForModel(modelID),
			remaining: bucket.RemainingFraction,
			resetTime: strings.TrimSpace(bucket.ResetTime),
		})
	}
	if len(entries) == 0 {
		return
	}

	quotas := make([]AntigravityModelQuota, 0, len(entries))
	for _, entry := range entries {
		quota := AntigravityModelQuota{Model: entry.id, DisplayName: entry.display, ResetTime: entry.resetTime}
		if entry.remaining != nil {
			frac := min(max(*entry.remaining, 0), 1)
			remPct := math.Round(frac*1000) / 10
			usedPct := math.Round((1-frac)*1000) / 10
			quota.RemainingFraction = &frac
			quota.RemainingPercent = &remPct
			quota.UsedPercent = &usedPct
		}
		quotas = append(quotas, quota)
	}

	slices.SortFunc(quotas, func(a, b AntigravityModelQuota) int {
		return strings.Compare(a.Model, b.Model)
	})
	out.Models = quotas
	// The native groups already separate the three allowances. Aggregating the
	// per-model quota on top of them would add a second Gemini card whose weekly
	// window mixes in the Argon models, so this stays a fallback for responses
	// that carry no Gemini group.
	fallback := antigravityModelGroup(entries)
	if fallback.GroupKey == "" || slices.ContainsFunc(out.QuotaGroups, func(group AntigravityQuotaGroup) bool {
		return group.GroupKey == fallback.GroupKey
	}) {
		return
	}
	out.QuotaGroups = append(out.QuotaGroups, fallback)
}

// antigravityModelGroup aggregates the per-model quotas into the Gemini group.
// The summary endpoint only reports the Claude/GPT allowance on older clients, so
// this is the fallback that keeps the Gemini windows on the console. Each window
// is bucketed by how far its reset time is from the earliest one: the upstream
// exposes a single reset time per window, five hours apart from the weekly one.
// Argon models are left out because their allowance is a group of its own.
func antigravityModelGroup(entries []antigravityModelEntry) AntigravityQuotaGroup {
	gemini := make([]antigravityModelEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.provider != antigravityGeminiAPIProvider || entry.remaining == nil || entry.resetTime == "" {
			continue
		}
		if strings.HasPrefix(entry.id, antigravityArgonModelPrefix) {
			continue
		}
		gemini = append(gemini, entry)
	}
	if len(gemini) == 0 {
		return AntigravityQuotaGroup{}
	}

	earliest := gemini[0].resetTime
	for _, entry := range gemini {
		if entry.resetTime < earliest {
			earliest = entry.resetTime
		}
	}

	type windowBucket struct {
		remaining *float64
		resetTime string
		models    []string
	}
	buckets := map[string]*windowBucket{}
	for _, entry := range gemini {
		window := "5h"
		if !antigravitySameQuotaWindow(earliest, entry.resetTime) {
			window = "weekly"
		}
		bucket, ok := buckets[window]
		if !ok {
			bucket = &windowBucket{}
			buckets[window] = bucket
		}
		if entry.display != "" {
			bucket.models = append(bucket.models, entry.display)
		}
		// The tightest remaining fraction in the window is the real constraint.
		if bucket.remaining == nil || *entry.remaining < *bucket.remaining {
			bucket.remaining = entry.remaining
			bucket.resetTime = entry.resetTime
		}
	}

	group := AntigravityQuotaGroup{
		GroupKey:    antigravityGroupGemini,
		DisplayName: "Gemini models",
	}
	for _, window := range []string{"5h", "weekly"} {
		bucket, ok := buckets[window]
		if !ok || bucket.remaining == nil {
			continue
		}
		label := "5h"
		if window == "weekly" {
			label = "Weekly"
		}
		group.Buckets = append(group.Buckets, AntigravityQuotaBucket{
			BucketID:          "models-" + window,
			DisplayName:       label,
			Window:            window,
			RemainingFraction: bucket.remaining,
			ResetTime:         bucket.resetTime,
		})
	}
	return group
}

// antigravitySameQuotaWindow reports whether two reset times belong to the same
// window: the five-hour window never exceeds five hours while the weekly one is
// at least a day away, so a 24-hour split separates them without guessing.
func antigravitySameQuotaWindow(earliest, candidate string) bool {
	earliestTime, err := time.Parse(time.RFC3339, earliest)
	if err != nil {
		return true
	}
	candidateTime, err := time.Parse(time.RFC3339, candidate)
	if err != nil {
		return true
	}
	diff := candidateTime.Sub(earliestTime)
	if diff < 0 {
		diff = -diff
	}
	return diff < 24*time.Hour
}

// antigravityAPIProviderForModel infers the provider of a retrieveUserQuota
// bucket, which carries no apiProvider field.
func antigravityAPIProviderForModel(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "gemini-"):
		return antigravityGeminiAPIProvider
	case strings.HasPrefix(modelID, "claude-"), strings.HasPrefix(modelID, "gpt-"):
		return "API_PROVIDER_ANTHROPIC"
	default:
		return ""
	}
}
