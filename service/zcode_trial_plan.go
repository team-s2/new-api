package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

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
		if channel.Status != common.ChannelStatusEnabled {
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

// detectZcodeTrialPlanModels queries the plan billing endpoint and returns the
// models an in-effect plan entitles. It returns an empty set, not an error, when
// the account has no active plan.
func detectZcodeTrialPlanModels(channel *model.Channel) (map[string]struct{}, error) {
	keys := channel.GetKeys()
	if len(keys) == 0 {
		return nil, errors.New("no credential")
	}
	token, err := ZcodeTrialPlanToken(keys[0])
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, "https://zcode.z.ai/api/v1/zcode-plan/billing/current", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Do not send the ZCode client User-Agent here. The billing endpoint answers
	// with an empty plan list when it sees that UA (it then expects the desktop's
	// own session), while a plain client sees the account's active plans.

	client := &http.Client{Timeout: zcodeTrialPlanDetectTimeout}
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
		return nil, fmt.Errorf("zcode trial plan: billing query returned status %d", resp.StatusCode)
	}
	return parseZcodeTrialPlanModels(body, time.Now())
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

// parseZcodeTrialPlanModels extracts the models of the entitlements that are in
// effect. Entitlement capabilities that are feature flags (code, search) rather
// than model names are skipped.
func parseZcodeTrialPlanModels(body []byte, now time.Time) (map[string]struct{}, error) {
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Plans []struct {
				StartsAt     int64 `json:"starts_at"`
				EndsAt       int64 `json:"ends_at"`
				Entitlements []struct {
					Capabilities []string `json:"capabilities"`
				} `json:"entitlements"`
			} `json:"plans"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, errors.New("zcode trial plan: invalid billing response")
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("zcode trial plan: billing response code %d: %s", parsed.Code, strings.TrimSpace(parsed.Msg))
	}

	models := make(map[string]struct{})
	for _, plan := range parsed.Data.Plans {
		if !zcodeTrialPlanInEffect(plan.StartsAt, plan.EndsAt, now) {
			continue
		}
		for _, entitlement := range plan.Entitlements {
			for _, capability := range entitlement.Capabilities {
				if modelName := zcodeTrialPlanModelName(capability); modelName != "" {
					models[modelName] = struct{}{}
				}
			}
		}
	}
	return models, nil
}

// zcodeTrialPlanInEffect reports whether a plan window covers now. The endpoint
// reports window bounds as Unix seconds.
func zcodeTrialPlanInEffect(startsAt, endsAt int64, now time.Time) bool {
	nowSeconds := now.Unix()
	if startsAt > 0 && startsAt > nowSeconds {
		return false
	}
	return endsAt == 0 || endsAt > nowSeconds
}

// zcodeTrialPlanModelName maps an entitlement capability to a model name.
// Capabilities are either "model:<name>" or a bare model name; anything else is
// a feature flag.
func zcodeTrialPlanModelName(capability string) string {
	trimmed := strings.ToLower(strings.TrimSpace(capability))
	if name, ok := strings.CutPrefix(trimmed, "model:"); ok {
		return name
	}
	if !zcodeTrialPlanModelLike(trimmed) {
		return ""
	}
	return trimmed
}

// zcodeTrialPlanModelLike separates model names from feature flags: a model name
// carries a version digit and a known family prefix.
func zcodeTrialPlanModelLike(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	if !strings.ContainsAny(value, "0123456789") {
		return false
	}
	for _, prefix := range []string{"glm", "claude", "gpt", "gemini", "chatglm", "codegeex"} {
		if strings.Contains(value, prefix) {
			return true
		}
	}
	return false
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
