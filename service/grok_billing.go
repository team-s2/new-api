package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Grok subscription billing probe against the CLI gateway. The endpoints are
// the ones the official Grok CLI uses (undocumented): /billing?format=credits
// returns the weekly credits window, /billing the monthly window. Both need
// the same CLI identity headers as chat traffic.
const (
	grokBillingWeeklyPath  = "/billing?format=credits"
	grokBillingMonthlyPath = "/billing"

	grokBillingTimeout = 15 * time.Second

	grokSuperGrokLimitCents      = 15_000  // $150.00
	grokSuperGrokHeavyLimitCents = 150_000 // $1,500.00
)

// GrokBillingUsage is the normalized usage view returned to the frontend.
// Weekly is the credits (rate-limit) window; Monthly is the dollar-denominated
// subscription window. Money fields are USD.
type GrokBillingUsage struct {
	Weekly  *GrokBillingWindow `json:"weekly,omitempty"`
	Monthly *GrokBillingWindow `json:"monthly,omitempty"`

	PrepaidBalance *float64 `json:"prepaid_balance,omitempty"`
	OnDemandCap    *float64 `json:"on_demand_cap,omitempty"`
	OnDemandUsed   *float64 `json:"on_demand_used,omitempty"`

	Plan                 string             `json:"plan,omitempty"`
	ProductUsage         []GrokProductUsage `json:"product_usage,omitempty"`
	IsUnifiedBillingUser bool               `json:"is_unified_billing_user,omitempty"`
	TopUpMethod          string             `json:"top_up_method,omitempty"`
	// FailedWindows names the windows ("weekly", "monthly") whose probe failed.
	FailedWindows []string `json:"failed_windows,omitempty"`
}

// GrokBillingWindow is one billing window with utilization and reset info.
// UsagePercent is the weekly credits utilization; UsedPercent the monthly one.
type GrokBillingWindow struct {
	PeriodType   string   `json:"period_type,omitempty"`
	PeriodStart  string   `json:"period_start,omitempty"`
	PeriodEnd    string   `json:"period_end,omitempty"`
	UsagePercent *float64 `json:"usage_percent,omitempty"`
	UsedPercent  *float64 `json:"used_percent,omitempty"`
	MonthlyLimit *float64 `json:"monthly_limit,omitempty"`
	MonthlyUsed  *float64 `json:"monthly_used,omitempty"`
}

// GrokProductUsage is a per-product usage row inside the weekly window.
type GrokProductUsage struct {
	Product      string   `json:"product"`
	UsagePercent *float64 `json:"usage_percent,omitempty"`
}

type grokBillingPayload struct {
	Config *grokBillingConfig `json:"config,omitempty"`
}

// grokBillingConfig is shared by both endpoints. Money fields arrive as
// {"val": N} objects or bare numbers: monthlyLimit/used are cents, while the
// credits-response prepaid/on-demand values are dollars.
type grokBillingConfig struct {
	CurrentPeriod *struct {
		Type  string `json:"type,omitempty"`
		Start string `json:"start,omitempty"`
		End   string `json:"end,omitempty"`
	} `json:"currentPeriod,omitempty"`
	CreditUsagePercent *float64 `json:"creditUsagePercent,omitempty"`
	ProductUsage       []struct {
		Product      string   `json:"product,omitempty"`
		UsagePercent *float64 `json:"usagePercent,omitempty"`
	} `json:"productUsage,omitempty"`
	MonthlyLimit         json.RawMessage `json:"monthlyLimit,omitempty"`
	Used                 json.RawMessage `json:"used,omitempty"`
	OnDemandCap          json.RawMessage `json:"onDemandCap,omitempty"`
	OnDemandUsed         json.RawMessage `json:"onDemandUsed,omitempty"`
	PrepaidBalance       json.RawMessage `json:"prepaidBalance,omitempty"`
	IsUnifiedBillingUser bool            `json:"isUnifiedBillingUser,omitempty"`
	TopUpMethod          string          `json:"topUpMethod,omitempty"`
	BillingPeriodStart   string          `json:"billingPeriodStart,omitempty"`
	BillingPeriodEnd     string          `json:"billingPeriodEnd,omitempty"`
}

// grokBillingAmount reads a {"val": N} object, bare number, or numeric string.
func grokBillingAmount(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return nil
	}
	if object, ok := value.(map[string]any); ok {
		value = object["val"]
	}
	switch typed := value.(type) {
	case float64:
		return &typed
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return &parsed
		}
	}
	return nil
}

// toUsage normalizes one billing response. Window detection follows the fields
// present rather than the endpoint, matching the official CLI's handling.
func (cfg *grokBillingConfig) toUsage() *GrokBillingUsage {
	usage := &GrokBillingUsage{
		PrepaidBalance:       grokBillingAmount(cfg.PrepaidBalance),
		OnDemandCap:          grokBillingAmount(cfg.OnDemandCap),
		OnDemandUsed:         grokBillingAmount(cfg.OnDemandUsed),
		IsUnifiedBillingUser: cfg.IsUnifiedBillingUser,
		TopUpMethod:          strings.TrimSpace(cfg.TopUpMethod),
	}
	for _, product := range cfg.ProductUsage {
		if name := strings.TrimSpace(product.Product); name != "" {
			usage.ProductUsage = append(usage.ProductUsage, GrokProductUsage{Product: name, UsagePercent: product.UsagePercent})
		}
	}

	periodType := ""
	if cfg.CurrentPeriod != nil {
		periodType = strings.ToLower(strings.TrimSpace(cfg.CurrentPeriod.Type))
	}
	hasWeekly := cfg.CreditUsagePercent != nil || strings.Contains(periodType, "weekly") || len(usage.ProductUsage) > 0 ||
		usage.PrepaidBalance != nil || usage.OnDemandCap != nil || usage.OnDemandUsed != nil
	if hasWeekly {
		// Weekly bounds must not fall back to the monthly billing period.
		usage.Weekly = &GrokBillingWindow{PeriodType: "weekly", UsagePercent: cfg.CreditUsagePercent}
		if cfg.CurrentPeriod != nil {
			usage.Weekly.PeriodStart = strings.TrimSpace(cfg.CurrentPeriod.Start)
			usage.Weekly.PeriodEnd = strings.TrimSpace(cfg.CurrentPeriod.End)
		}
	}

	limitCents := grokBillingAmount(cfg.MonthlyLimit)
	usedCents := grokBillingAmount(cfg.Used)
	if limitCents != nil || usedCents != nil || (!hasWeekly && strings.TrimSpace(cfg.BillingPeriodEnd) != "") {
		monthly := &GrokBillingWindow{
			PeriodType:  "monthly",
			PeriodStart: strings.TrimSpace(cfg.BillingPeriodStart),
			PeriodEnd:   strings.TrimSpace(cfg.BillingPeriodEnd),
		}
		if limitCents != nil {
			limit := *limitCents / 100
			monthly.MonthlyLimit = &limit
			switch math.Round(*limitCents) {
			case grokSuperGrokLimitCents:
				usage.Plan = "SuperGrok"
			case grokSuperGrokHeavyLimitCents:
				usage.Plan = "SuperGrok Heavy"
			}
		}
		if usedCents != nil {
			used := *usedCents / 100
			monthly.MonthlyUsed = &used
			if limitCents != nil && *limitCents > 0 {
				// Overage beyond the included allowance is billed on demand.
				percent := min(*usedCents, *limitCents) / *limitCents * 100
				monthly.UsedPercent = &percent
			}
		}
		usage.Monthly = monthly
	}
	return usage
}

// mergeGrokBillingUsage takes the weekly window and credit balances from the
// credits response and the monthly window and plan from the monthly response,
// falling back to whichever response carried a field.
func mergeGrokBillingUsage(weekly, monthly *GrokBillingUsage) *GrokBillingUsage {
	if weekly == nil {
		return monthly
	}
	if monthly == nil {
		return weekly
	}
	merged := *weekly
	if monthly.Monthly != nil {
		merged.Monthly = monthly.Monthly
	}
	if monthly.Plan != "" {
		merged.Plan = monthly.Plan
	}
	if merged.Weekly == nil {
		merged.Weekly = monthly.Weekly
	}
	if len(merged.ProductUsage) == 0 {
		merged.ProductUsage = monthly.ProductUsage
	}
	if merged.PrepaidBalance == nil {
		merged.PrepaidBalance = monthly.PrepaidBalance
	}
	if merged.OnDemandCap == nil {
		merged.OnDemandCap = monthly.OnDemandCap
	}
	if merged.OnDemandUsed == nil {
		merged.OnDemandUsed = monthly.OnDemandUsed
	}
	if merged.TopUpMethod == "" {
		merged.TopUpMethod = monthly.TopUpMethod
	}
	merged.IsUnifiedBillingUser = merged.IsUnifiedBillingUser || monthly.IsUnifiedBillingUser
	return &merged
}

// GrokBillingTimeout exposes the probe timeout for the controller's context.
func GrokBillingTimeout() time.Duration { return grokBillingTimeout }

// FetchGrokBillingUsage probes both billing windows. A window that fails is
// listed in FailedWindows; when both fail the usage is nil and the returned
// status is the upstream status (401 preferred so callers can refresh the
// token). Transport errors are returned as err.
func FetchGrokBillingUsage(ctx context.Context, client *http.Client, baseURL, accessToken string) (*GrokBillingUsage, int, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://" + GrokCLIProxyHost + "/v1"
	}

	weekly, weeklyStatus, err := fetchGrokBillingWindow(ctx, client, base, grokBillingWeeklyPath, accessToken)
	if err != nil {
		return nil, 0, err
	}
	monthly, monthlyStatus, err := fetchGrokBillingWindow(ctx, client, base, grokBillingMonthlyPath, accessToken)
	if err != nil {
		return nil, 0, err
	}

	usage := mergeGrokBillingUsage(weekly, monthly)
	if usage == nil {
		if weeklyStatus == http.StatusUnauthorized || monthlyStatus == http.StatusUnauthorized {
			return nil, http.StatusUnauthorized, nil
		}
		if weeklyStatus != http.StatusOK {
			return nil, weeklyStatus, nil
		}
		return nil, monthlyStatus, nil
	}
	if weeklyStatus != http.StatusOK {
		usage.FailedWindows = append(usage.FailedWindows, "weekly")
	}
	if monthlyStatus != http.StatusOK {
		usage.FailedWindows = append(usage.FailedWindows, "monthly")
	}
	return usage, http.StatusOK, nil
}

// fetchGrokBillingWindow returns nil usage with the upstream status for
// non-200 responses; only transport and decode failures are errors.
func fetchGrokBillingWindow(ctx context.Context, client *http.Client, base, path, accessToken string) (*GrokBillingUsage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	ApplyGrokCLIHeaders(req.Header, base)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	var payload grokBillingPayload
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("grok billing: decode response: %w", err)
	}
	if payload.Config == nil {
		return nil, resp.StatusCode, nil
	}
	return payload.Config.toUsage(), resp.StatusCode, nil
}
