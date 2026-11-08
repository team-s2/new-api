package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// RefreshAntigravityChannelCredential forces a token refresh unless a
// concurrent caller has already replaced the credential this call observed,
// then resolves any email or Cloud Code project the credential still lacks.
func RefreshAntigravityChannelCredential(ctx context.Context, channelID int) (*AntigravityOAuthKey, *model.Channel, error) {
	if channelID <= 0 {
		return nil, nil, fmt.Errorf("invalid channel id: %d", channelID)
	}
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	key, updated, err := refreshAntigravityChannelCredential(ctx, ch, true)
	if err != nil {
		return nil, nil, err
	}
	key, updated = backfillAntigravityKeyMetadata(ctx, updated, key)
	return key, updated, nil
}

// refreshAntigravityChannelCredential rotates the OAuth token under the channel
// row lock. Only the token endpoint is called inside the lock; metadata lookups
// (which may retry onboarding for several seconds) belong in
// backfillAntigravityKeyMetadata.
func refreshAntigravityChannelCredential(ctx context.Context, observed *model.Channel, force bool) (*AntigravityOAuthKey, *model.Channel, error) {
	if observed == nil {
		return nil, nil, fmt.Errorf("channel is nil")
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var oauthKey *AntigravityOAuthKey
	ch, err := model.UpdateChannelCredential(refreshCtx, observed.Id, func(current *model.Channel) (string, error) {
		if current.Type != constant.ChannelTypeAntigravitySub || current.ChannelInfo.IsMultiKey {
			return "", fmt.Errorf("channel %d is not a single-key Antigravity subscription channel", current.Id)
		}

		existingKey, ok := ParseAntigravityOAuthKey(current.Key)
		if !ok {
			return "", fmt.Errorf("channel %d key is not a valid Antigravity OAuth JSON credential", current.Id)
		}
		if current.Key != observed.Key || (!force && grokTokenUsable(existingKey.Expired)) {
			oauthKey = existingKey
			return current.Key, nil
		}
		if existingKey.RefreshToken == "" {
			return "", fmt.Errorf("channel %d key does not contain refresh_token", current.Id)
		}

		refreshedKey, err := RefreshAntigravityOAuthToken(refreshCtx, existingKey.RefreshToken, current.GetSetting().Proxy)
		if err != nil {
			return "", err
		}
		merged, err := mergeAntigravityKeyJSON(current.Key, refreshedKey)
		if err != nil {
			return "", err
		}
		oauthKey, _ = ParseAntigravityOAuthKey(merged)
		return merged, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return oauthKey, ch, nil
}

// backfillAntigravityKeyMetadata resolves the account email and Cloud Code
// project that the OAuth exchange could not determine. The upstream lookups run
// outside the channel row lock and only the merge is transactional. Failures
// keep the current credential; the next refresh tries again.
func backfillAntigravityKeyMetadata(ctx context.Context, ch *model.Channel, key *AntigravityOAuthKey) (*AntigravityOAuthKey, *model.Channel) {
	if ch == nil || key == nil || (key.Email != "" && key.ProjectID != "") {
		return key, ch
	}
	lookupCtx, cancel := context.WithTimeout(ctx, antigravityOAuthHTTPTimeout)
	defer cancel()

	proxyURL := ch.GetSetting().Proxy
	metadata := &AntigravityOAuthKey{}
	if key.Email == "" {
		if email, err := fetchAntigravityUserEmail(lookupCtx, key.AccessToken, proxyURL); err == nil {
			metadata.Email = email
		}
	}
	if key.ProjectID == "" {
		if projectID, planTier, err := FetchAntigravityProjectAndTier(lookupCtx, key.AccessToken, proxyURL); err == nil {
			metadata.ProjectID = projectID
			metadata.PlanTier = planTier
		}
	}
	if metadata.Email == "" && metadata.ProjectID == "" && metadata.PlanTier == "" {
		return key, ch
	}

	var mergedKey *AntigravityOAuthKey
	updated, err := model.UpdateChannelCredential(lookupCtx, ch.Id, func(current *model.Channel) (string, error) {
		if current.Type != constant.ChannelTypeAntigravitySub || current.ChannelInfo.IsMultiKey {
			return "", fmt.Errorf("channel %d is not a single-key Antigravity subscription channel", current.Id)
		}
		currentKey, ok := ParseAntigravityOAuthKey(current.Key)
		if !ok {
			return "", fmt.Errorf("channel %d key is not a valid Antigravity OAuth JSON credential", current.Id)
		}
		// A login, token rotation, or another metadata lookup may have replaced
		// the credential while the network requests ran outside the row lock.
		if current.Key != ch.Key {
			mergedKey = currentKey
			return current.Key, nil
		}
		if currentKey.Email != "" {
			metadata.Email = ""
		}
		if currentKey.ProjectID != "" {
			metadata.ProjectID = ""
		}
		if currentKey.PlanTier != "" {
			metadata.PlanTier = ""
		}
		merged, err := mergeAntigravityKeyJSON(current.Key, metadata)
		if err != nil {
			return "", err
		}
		mergedKey, _ = ParseAntigravityOAuthKey(merged)
		return merged, nil
	})
	if err != nil || mergedKey == nil {
		return key, ch
	}
	return mergedKey, updated
}

// EnsureAntigravityChannelAccessToken returns the channel key JSON with a
// usable access token, lazily refreshing it when it is expired or within the
// refresh skew. The channel is never mutated because callers may hold the
// shared channel cache entry. On refresh failure the stored key is returned so
// the relay can still try it.
func EnsureAntigravityChannelAccessToken(ctx context.Context, ch *model.Channel) (string, error) {
	if ch == nil {
		return "", fmt.Errorf("channel is nil")
	}
	rawKey := strings.TrimSpace(ch.Key)
	parsed, ok := ParseAntigravityOAuthKey(rawKey)
	if !ok {
		if _, err := ExtractAntigravityAccessToken(rawKey); err != nil {
			return "", err
		}
		return rawKey, nil
	}
	if parsed.RefreshToken == "" || ch.Id <= 0 || ch.ChannelInfo.IsMultiKey || grokTokenUsable(parsed.Expired) {
		return rawKey, nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, updated, err := refreshAntigravityChannelCredential(refreshCtx, ch, false)
	if err != nil {
		common.SysError(fmt.Sprintf("antigravity channel #%d lazy token refresh failed: %v", ch.Id, err))
		return rawKey, nil
	}
	return updated.Key, nil
}

// mergeAntigravityKeyJSON overlays the non-empty fields of update onto the
// stored key JSON, preserving fields this package does not model.
func mergeAntigravityKeyJSON(oldKey string, update *AntigravityOAuthKey) (string, error) {
	keyMap := make(map[string]any)
	trimmed := strings.TrimSpace(oldKey)
	if strings.HasPrefix(trimmed, "{") {
		_ = common.Unmarshal([]byte(trimmed), &keyMap)
	}
	for field, value := range map[string]string{
		"access_token":  update.AccessToken,
		"refresh_token": update.RefreshToken,
		"project_id":    update.ProjectID,
		"email":         update.Email,
		"plan_tier":     update.PlanTier,
		"last_refresh":  update.LastRefresh,
		"expired":       update.Expired,
	} {
		if value != "" {
			keyMap[field] = value
		}
	}
	out, err := common.Marshal(keyMap)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
