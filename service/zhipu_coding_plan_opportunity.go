package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/google/uuid"
)

// Reset-card claiming mirrors the official ZCode client: it polls every five
// minutes and asks /reset/opportunity whether the account earned a card once
// the previous answer's cooldown has passed.
const (
	zhipuOpportunityTickInterval = 5 * time.Minute
	// zhipuOpportunityCooldown follows a grant, a throttle, or a stable error.
	zhipuOpportunityCooldown = 10 * time.Minute
	// zhipuOpportunityRetryInterval follows an unknown outcome, and is also the
	// earliest retry after a "not eligible yet" answer.
	zhipuOpportunityRetryInterval = 5 * time.Minute
	zhipuOpportunityRequestBudget = 30 * time.Second

	zhipuOpportunityNotEligibleCode = 3301
	zhipuOpportunityDependencyCode  = 2007
)

var zhipuResetOpportunityTaskOnce sync.Once

// ZhipuResetCardClaim reports one successful claim. FiveHour and Week are the
// net inventory increase, valid only when CountsKnown is true.
type ZhipuResetCardClaim struct {
	Channel     *model.Channel
	FiveHour    int
	Week        int
	CountsKnown bool
}

// ZhipuResetNotifier announces the claims one pass made.
type ZhipuResetNotifier func(claims []ZhipuResetCardClaim)

// zhipuResetOpportunity is one account's claim schedule, kept in memory only.
// The single task goroutine owns it.
type zhipuResetOpportunity struct {
	nextAt time.Time
	// retryKey is reused only after an unknown outcome, so a retry cannot be
	// granted twice for the same attempt.
	retryKey string
}

// StartZhipuResetOpportunityTask claims reset cards for Coding Plan channels,
// including ones the peak task paused, so cards accrue without the ZCode
// desktop client. It never spends a card or changes channel status.
func StartZhipuResetOpportunityTask(notify ZhipuResetNotifier) {
	zhipuResetOpportunityTaskOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("Zhipu reset opportunity task started: tick=%s", zhipuOpportunityTickInterval))
			// Keyed by a hash of the ZCode JWT so channels sharing an account share
			// its schedule, without keeping the token itself.
			states := make(map[[32]byte]*zhipuResetOpportunity)
			ticker := time.NewTicker(zhipuOpportunityTickInterval)
			defer ticker.Stop()

			runZhipuResetOpportunityOnce(states, notify)
			for range ticker.C {
				runZhipuResetOpportunityOnce(states, notify)
			}
		})
	})
}

// runZhipuResetOpportunityOnce claims for every due account, one at a time.
func runZhipuResetOpportunityOnce(states map[[32]byte]*zhipuResetOpportunity, notify ZhipuResetNotifier) {
	channels, err := model.GetZhipuResetOpportunitySnapshots()
	if err != nil {
		logger.LogWarn(context.Background(), common.LogText("Zhipu reset opportunity task failed to load channels: %v", err))
		return
	}

	seen := make(map[[32]byte]bool, len(channels))
	var claims []ZhipuResetCardClaim
	for _, channel := range channels {
		// Skip channels an administrator or a failure disabled; a peak pause
		// still claims.
		if channel.ChannelInfo.IsMultiKey || channel.Status != common.ChannelStatusEnabled && channel.StatusReason() != model.ChannelStatusReasonZhipuPeak {
			continue
		}
		// The OAuth credential format lives in relay/channel/zhipu_4v, which
		// imports this package; read only the two tokens the reset API needs.
		var credential struct {
			AccessToken string `json:"access_token"`
			ZcodeJWT    string `json:"zcode_jwt"`
		}
		if common.UnmarshalJsonStr(channel.Key, &credential) != nil || strings.TrimSpace(credential.ZcodeJWT) == "" {
			continue
		}
		key := sha256.Sum256([]byte(strings.TrimSpace(credential.ZcodeJWT)))
		seen[key] = true
		state := states[key]
		if state == nil {
			state = &zhipuResetOpportunity{}
			states[key] = state
		}
		if time.Now().Before(state.nextAt) {
			continue
		}
		if claim, granted := claimZhipuResetOpportunity(state, channel, credential.ZcodeJWT, credential.AccessToken); granted {
			claims = append(claims, claim)
		}
	}
	maps.DeleteFunc(states, func(key [32]byte, _ *zhipuResetOpportunity) bool { return !seen[key] })

	if len(claims) > 0 && notify != nil {
		notify(claims)
	}
}

// claimZhipuResetOpportunity asks for one account's reset card through the
// channel's proxy and measures what a grant added to the inventory.
func claimZhipuResetOpportunity(state *zhipuResetOpportunity, channel *model.Channel, zcodeJWT, accessToken string) (ZhipuResetCardClaim, bool) {
	// Channel.GetSetting saves the record when the setting is unreadable, which
	// would wipe the columns this snapshot leaves out.
	var setting dto.ChannelSettings
	if channel.Setting != nil && common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
		state.nextAt = time.Now().Add(zhipuOpportunityRetryInterval)
		logger.LogWarn(context.Background(), common.LogText("Zhipu reset opportunity skipped channel %d: invalid channel setting", channel.Id))
		return ZhipuResetCardClaim{}, false
	}
	client, err := GetHttpClientWithProxy(setting.Proxy)
	if err != nil {
		state.nextAt = time.Now().Add(zhipuOpportunityRetryInterval)
		logger.LogWarn(context.Background(), common.LogText("Zhipu reset opportunity skipped channel %d: %v", channel.Id, err))
		return ZhipuResetCardClaim{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), zhipuOpportunityRequestBudget)
	defer cancel()
	// The official client also reads the inventory every tick before asking.
	before, beforeErr := FetchZhipuCodingPlanResetStatus(ctx, client, zcodeJWT, accessToken)
	granted, err := state.claim(ctx, client, zcodeJWT, accessToken)
	if err != nil {
		logger.LogWarn(ctx, common.LogText("Zhipu reset opportunity failed for channel %d: %v", channel.Id, err))
		return ZhipuResetCardClaim{}, false
	}
	if !granted {
		return ZhipuResetCardClaim{}, false
	}
	logger.LogInfo(ctx, common.LogText("Zhipu reset card granted for channel %d", channel.Id))

	claim := ZhipuResetCardClaim{Channel: channel}
	after, afterErr := FetchZhipuCodingPlanResetStatus(ctx, client, zcodeJWT, accessToken)
	if err := errors.Join(beforeErr, afterErr); err != nil {
		logger.LogWarn(ctx, common.LogText("Zhipu reset inventory check failed for channel %d: %v", channel.Id, err))
		return claim, true
	}
	claim.FiveHour, claim.Week, claim.CountsKnown = zhipuResetGrantedCards(before, after, time.Now())
	return claim, true
}

// claim calls /reset/opportunity once and schedules the next attempt the way
// the official client does. A "not eligible yet" or throttled answer is a
// normal outcome, not an error.
func (s *zhipuResetOpportunity) claim(ctx context.Context, client *http.Client, zcodeJWT, accessToken string) (bool, error) {
	if err := validateZhipuResetCredentials(zcodeJWT, accessToken); err != nil {
		s.nextAt = time.Now().Add(zhipuOpportunityCooldown)
		return false, err
	}
	if s.retryKey == "" {
		s.retryKey = uuid.NewString()
	}
	body, err := common.Marshal(map[string]string{"idempotency_key": s.retryKey})
	if err != nil {
		return false, err
	}
	var result struct {
		Granted bool `json:"granted"`
	}
	err = doZhipuResetRequest(ctx, client, http.MethodPost, zhipuResetBaseURL+"/api/v1/coding-plan/reset/opportunity", body, zcodeJWT, accessToken, &result)
	now := time.Now()

	var upstream *zhipuResetError
	if err != nil && (!errors.As(err, &upstream) || upstream.businessCode == zhipuOpportunityDependencyCode) {
		// Transport failure, timeout, or upstream dependency failure: the
		// outcome is unknown, so retry with the same key.
		s.nextAt = now.Add(zhipuOpportunityRetryInterval)
		return false, err
	}
	// Any answer the API gave settles this attempt.
	s.retryKey = ""
	s.nextAt = now.Add(zhipuOpportunityCooldown)
	switch {
	case err == nil:
		return result.Granted, nil
	case upstream.businessCode == zhipuOpportunityNotEligibleCode:
		var denied struct {
			NextTryAt int64 `json:"next_try_at"`
		}
		if common.Unmarshal(upstream.data, &denied) != nil || denied.NextTryAt <= 0 {
			return false, errors.New("zhipu coding plan reset: not-eligible answer has no valid next_try_at")
		}
		s.nextAt = time.UnixMilli(denied.NextTryAt)
		if earliest := now.Add(zhipuOpportunityRetryInterval); s.nextAt.Before(earliest) {
			s.nextAt = earliest
		}
		return false, nil
	case upstream.businessCode == http.StatusTooManyRequests,
		upstream.businessCode == 0 && upstream.httpStatus == http.StatusTooManyRequests:
		return false, nil
	default:
		return false, err
	}
}

// zhipuResetGrantedCards is the net inventory increase between two snapshots
// around a grant. The count is unknown when a card expired or was spent in
// between, since either hides part of the increase.
func zhipuResetGrantedCards(before, after *ZhipuCodingPlanResetStatus, now time.Time) (fiveHour, week int, known bool) {
	fiveHour = len(after.AvailableFiveHourResets) - len(before.AvailableFiveHourResets)
	week = len(after.AvailableWeekResets) - len(before.AvailableWeekResets)
	if fiveHour < 0 || week < 0 || fiveHour+week == 0 {
		return 0, 0, false
	}
	nowMilli := now.UnixMilli()
	for _, card := range slices.Concat(before.AvailableFiveHourResets, before.AvailableWeekResets) {
		if card.ExpireAt <= nowMilli {
			return 0, 0, false
		}
	}
	if zhipuResetUsedBetween(before.LatestFiveHourReset, after.LatestFiveHourReset) ||
		zhipuResetUsedBetween(before.LatestWeekReset, after.LatestWeekReset) {
		return 0, 0, false
	}
	return fiveHour, week, true
}

// zhipuResetUsedBetween reports whether a card of one window was spent between
// two snapshots of its latest usage.
func zhipuResetUsedBetween(before, after *ZhipuCodingPlanResetHistory) bool {
	return after != nil && (before == nil || before.UsedAt != after.UsedAt)
}
