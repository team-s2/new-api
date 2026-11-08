package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"
)

// zcodeTrialPlanBalanceURL is the authoritative source of plan availability;
// the official client has deprecated billing/current.
const zcodeTrialPlanBalanceURL = "https://zcode.z.ai/api/v1/zcode-plan/billing/balance"

const (
	// zcodeTrialPlanRefreshInterval is how often the trial plan availability is
	// re-checked. The plan changes at minute granularity at most, so a frequent
	// poll only spends quota lookups.
	zcodeTrialPlanRefreshInterval = 10 * time.Minute
	zcodeTrialPlanDetectTimeout   = 15 * time.Second
	// zcodeTrialPlanMaxConcurrency caps the requests relayed through one trial
	// plan channel; the plan is metered as a single subscription.
	zcodeTrialPlanMaxConcurrency = 3
)

var zcodeTrialPlanTaskOnce sync.Once

// zcodeTrialPlanAvailability is the set of models a channel's trial plan covers
// right now, keyed by request-facing model name.
type zcodeTrialPlanAvailability struct {
	models map[string]struct{}
}

var zcodeTrialPlanState = struct {
	sync.RWMutex
	byChannel map[int]*zcodeTrialPlanAvailability
}{byChannel: map[int]*zcodeTrialPlanAvailability{}}

var zcodeTrialPlanInflight = struct {
	sync.Mutex
	counts map[int]int
}{counts: map[int]int{}}

// StartZcodeTrialPlanTask refreshes which trial plan channels are usable. Every
// node tracks the plans it routes to itself, so the state needs no shared store
// and a single node's restart never leaves a stale "available" behind.
func StartZcodeTrialPlanTask() {
	zcodeTrialPlanTaskOnce.Do(func() {
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("ZCode trial plan task started: tick=%s", zcodeTrialPlanRefreshInterval))
			ticker := time.NewTicker(zcodeTrialPlanRefreshInterval)
			defer ticker.Stop()

			refreshZcodeTrialPlanAvailability()
			for range ticker.C {
				refreshZcodeTrialPlanAvailability()
			}
		})
	})
}

// refreshZcodeTrialPlanAvailability rebuilds the availability of every trial
// plan channel. A channel whose plan cannot be verified is treated as
// unavailable, so traffic falls back to the ordinary channels instead of
// failing against a plan without quota.
func refreshZcodeTrialPlanAvailability() {
	channels, err := model.GetZcodeTrialPlanChannels()
	if err != nil {
		logger.LogWarn(context.Background(), common.LogText("ZCode trial plan task failed to load channels: %v", err))
		return
	}

	detected := make(map[int]*zcodeTrialPlanAvailability, len(channels))
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled || !channel.IsZcodeTrialPlan() {
			continue
		}
		models, err := detectZcodeTrialPlanModels(channel)
		if err != nil {
			logger.LogWarn(context.Background(), common.LogText("ZCode trial plan detection failed for channel #%d: %v", channel.Id, err))
			continue
		}
		if len(models) == 0 {
			continue
		}
		detected[channel.Id] = &zcodeTrialPlanAvailability{models: models}
	}

	zcodeTrialPlanState.Lock()
	zcodeTrialPlanState.byChannel = detected
	zcodeTrialPlanState.Unlock()
}

// detectZcodeTrialPlanModels asks the plan balance endpoint which models the
// channel's plan covers, the way the official client does. It returns an empty
// set, not an error, when the account has no active plan.
func detectZcodeTrialPlanModels(channel *model.Channel) (map[string]struct{}, error) {
	keys := channel.GetKeys()
	if len(keys) == 0 {
		return nil, errors.New("no credential")
	}
	token, err := ZcodeTrialPlanToken(keys[0])
	if err != nil {
		return nil, err
	}
	// Channel.GetSetting saves the record when the setting is unreadable, which
	// would wipe the columns this snapshot leaves out.
	var setting dto.ChannelSettings
	if channel.Setting != nil && common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
		return nil, errors.New("zcode trial plan: invalid channel setting")
	}
	client, err := GetHttpClientWithProxy(setting.Proxy)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), zcodeTrialPlanDetectTimeout)
	defer cancel()
	requestURL := zcodeTrialPlanBalanceURL + "?" + url.Values{"app_version": {constant.ZcodeClientVersion}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	setZcodeSourceHeaders(req.Header, token)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zcode trial plan: balance query returned status %d", resp.StatusCode)
	}
	// A missing or malformed Date leaves the zero time, and parsing falls back.
	responseTime, _ := http.ParseTime(resp.Header.Get("Date"))
	return parseZcodeTrialPlanModels(body, responseTime)
}

// ZcodeTrialPlanToken returns the JWT that authenticates the trial plan relay. A
// credential that is not a JSON object is used as the token verbatim.
func ZcodeTrialPlanToken(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("zcode trial plan: empty credential")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed, nil
	}

	var credential struct {
		ZcodeJWT    string `json:"zcode_jwt"`
		AccessToken string `json:"access_token"`
		APIKey      string `json:"api_key"`
	}
	if err := common.Unmarshal([]byte(trimmed), &credential); err != nil {
		return "", errors.New("zcode trial plan: invalid credential json")
	}
	for _, token := range []string{credential.ZcodeJWT, credential.AccessToken, credential.APIKey} {
		if token = strings.TrimSpace(token); token != "" {
			return token, nil
		}
	}
	return "", errors.New("zcode trial plan: credential is missing zcode_jwt, please run OAuth login again")
}

// zcodeTrialPlan is one plan instance of the balance response. EndsAt is a Unix
// second bound the endpoint may send as a number or a string.
type zcodeTrialPlan struct {
	UserPlanID string `json:"user_plan_id"`
	PlanID     string `json:"plan_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	EndsAt     any    `json:"ends_at"`
}

// zcodeTrialPlanBalance is one quota bucket of the balance response.
type zcodeTrialPlanBalance struct {
	UserPlanID   string   `json:"user_plan_id"`
	PlanID       string   `json:"plan_id"`
	ShowName     string   `json:"show_name"`
	Capabilities []string `json:"capabilities"`
}

// parseZcodeTrialPlanModels applies the official client's balance rules: plans
// past their end count as expired, an active Start Plan is required, and the
// models come from the balance buckets of plans that have not expired. The
// official client also treats an active plan without models as usable, but
// routing here needs the model list, so that case yields an empty set.
func parseZcodeTrialPlanModels(body []byte, responseTime time.Time) (map[string]struct{}, error) {
	var parsed struct {
		Code    *int   `json:"code"`
		Success *bool  `json:"success"`
		Msg     string `json:"msg"`
		Data    struct {
			ServerTime *float64                `json:"server_time"`
			Plans      []zcodeTrialPlan        `json:"plans"`
			Balances   []zcodeTrialPlanBalance `json:"balances"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("zcode trial plan: invalid balance response")
	}
	if parsed.Success != nil && !*parsed.Success ||
		parsed.Code != nil && *parsed.Code != 0 && *parsed.Code != http.StatusOK {
		return nil, fmt.Errorf("zcode trial plan: balance response rejected: code %d: %s", lo.FromPtr(parsed.Code), strings.TrimSpace(parsed.Msg))
	}

	now := float64(time.Now().Unix())
	if !responseTime.IsZero() {
		now = float64(responseTime.Unix())
	} else if parsed.Data.ServerTime != nil && *parsed.Data.ServerTime >= 0 {
		now = *parsed.Data.ServerTime
	}
	plans := parsed.Data.Plans
	for i := range plans {
		if end := zcodeTrialPlanEnd(plans[i].EndsAt); zcodeTrialPlanActive(plans[i]) && end > 0 && end <= now {
			plans[i].Status = "expired"
		}
	}

	models := make(map[string]struct{})
	if !slices.ContainsFunc(plans, zcodeStartPlanActive) {
		return models, nil
	}
	for _, balance := range parsed.Data.Balances {
		if !zcodeTrialPlanBalanceLive(balance, plans) {
			continue
		}
		for _, name := range zcodeTrialPlanBalanceModels(balance) {
			models[strings.ToLower(name)] = struct{}{}
		}
	}
	return models, nil
}

// zcodeTrialPlanEnd reads a plan's end bound; 0 means it has none.
func zcodeTrialPlanEnd(value any) float64 {
	switch end := value.(type) {
	case float64:
		return end
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(end), 64)
		return parsed
	}
	return 0
}

func zcodeTrialPlanActive(plan zcodeTrialPlan) bool {
	return strings.ToLower(strings.TrimSpace(plan.Status)) == "active"
}

// zcodeStartPlanActive reports whether a plan is an active Start Plan. A plan
// with neither an ID nor a name is accepted, as the official client does.
func zcodeStartPlanActive(plan zcodeTrialPlan) bool {
	if !zcodeTrialPlanActive(plan) {
		return false
	}
	planID := strings.ToLower(strings.TrimSpace(plan.PlanID))
	name := strings.ToLower(strings.TrimSpace(plan.Name))
	if planID == "" && name == "" {
		return true
	}
	return strings.Contains(planID, "start-plan") || strings.Contains(planID, "start plan") ||
		strings.Contains(name, "start-plan") || strings.Contains(name, "start plan")
}

// zcodeTrialPlanBalanceLive drops buckets whose owning plans have all expired.
// A bucket with no identifiable owner is kept.
func zcodeTrialPlanBalanceLive(balance zcodeTrialPlanBalance, plans []zcodeTrialPlan) bool {
	owned := false
	for _, plan := range plans {
		owner := plan.PlanID == balance.PlanID
		if balance.UserPlanID != "" && plan.UserPlanID != "" {
			owner = plan.UserPlanID == balance.UserPlanID
		}
		if !owner {
			continue
		}
		owned = true
		if strings.ToLower(strings.TrimSpace(plan.Status)) != "expired" {
			return true
		}
	}
	return !owned
}

// zcodeTrialPlanBalanceModels lists a bucket's models: its "model:<name>"
// capabilities, or its show name when it has none.
func zcodeTrialPlanBalanceModels(balance zcodeTrialPlanBalance) []string {
	var names []string
	for _, capability := range balance.Capabilities {
		trimmed := strings.TrimSpace(capability)
		if len(trimmed) < len("model:") || !strings.EqualFold(trimmed[:len("model:")], "model:") {
			continue
		}
		if name := strings.TrimSpace(trimmed[len("model:"):]); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		return names
	}
	if name := strings.TrimSpace(balance.ShowName); name != "" {
		return []string{name}
	}
	return nil
}

// ZcodeTrialPlanAvailable reports whether the channel's trial plan currently
// covers modelName.
func ZcodeTrialPlanAvailable(channelID int, modelName string) bool {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if modelName == "" {
		return false
	}

	zcodeTrialPlanState.RLock()
	availability := zcodeTrialPlanState.byChannel[channelID]
	zcodeTrialPlanState.RUnlock()
	if availability == nil {
		return false
	}
	_, covered := availability.models[modelName]
	return covered
}

// ZcodeTrialPlanAcquire reserves one of the plan's concurrency slots. It never
// blocks: a request that finds no free slot falls back to another channel.
func ZcodeTrialPlanAcquire(channelID int) bool {
	zcodeTrialPlanInflight.Lock()
	defer zcodeTrialPlanInflight.Unlock()
	if zcodeTrialPlanInflight.counts[channelID] >= zcodeTrialPlanMaxConcurrency {
		return false
	}
	zcodeTrialPlanInflight.counts[channelID]++
	return true
}

// ZcodeTrialPlanRelease frees the slot acquired for the channel.
func ZcodeTrialPlanRelease(channelID int) {
	zcodeTrialPlanInflight.Lock()
	defer zcodeTrialPlanInflight.Unlock()
	if zcodeTrialPlanInflight.counts[channelID] <= 1 {
		delete(zcodeTrialPlanInflight.counts, channelID)
		return
	}
	zcodeTrialPlanInflight.counts[channelID]--
}

// ZcodeTrialPlanSlotFree reports whether the channel has a free plan slot.
func ZcodeTrialPlanSlotFree(channelID int) bool {
	zcodeTrialPlanInflight.Lock()
	defer zcodeTrialPlanInflight.Unlock()
	return zcodeTrialPlanInflight.counts[channelID] < zcodeTrialPlanMaxConcurrency
}

// ZcodeTrialPlanPreferredChannel returns the trial plan channel that should
// serve this request ahead of the ordinary channels, or nil when no plan applies.
func ZcodeTrialPlanPreferredChannel(param *RetryParam) *model.Channel {
	if param == nil || param.Ctx == nil || param.TokenGroup == "auto" || param.GetRetry() != 0 {
		return nil
	}
	if len(GetChannelConstraints(param.Ctx).Pins) > 0 {
		return nil
	}
	for _, channel := range model.GetEnabledChannelsForGroupModel(param.TokenGroup, param.ModelName) {
		if !channel.IsZcodeTrialPlan() {
			continue
		}
		if !ZcodeTrialPlanAvailable(channel.Id, param.ModelName) || !ZcodeTrialPlanSlotFree(channel.Id) {
			continue
		}
		return channel
	}
	return nil
}
