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

type AntigravityCredentialRefreshOptions struct {
	ResetCaches bool
}

func RefreshAntigravityChannelCredential(ctx context.Context, channelID int, opts AntigravityCredentialRefreshOptions) (*AntigravityOAuthKey, *model.Channel, error) {
	if channelID <= 0 {
		return nil, nil, fmt.Errorf("invalid channel id: %d", channelID)
	}
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	return refreshAntigravityChannelCredential(ctx, ch, true)
}

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
		if existingKey.AccessToken != "" && (current.Key != observed.Key || (!force && grokTokenUsable(existingKey.Expired))) {
			oauthKey = existingKey
			return current.Key, nil
		}
		if existingKey.RefreshToken == "" {
			return "", fmt.Errorf("channel %d key does not contain refresh_token", current.Id)
		}

		proxyURL := strings.TrimSpace(current.GetSetting().Proxy)
		refreshedKey, err := RefreshAntigravityOAuthToken(refreshCtx, existingKey.RefreshToken, proxyURL)
		if err != nil {
			return "", err
		}

		refreshedKey.Email = existingKey.Email
		refreshedKey.ProjectID = existingKey.ProjectID
		refreshedKey.PlanTier = existingKey.PlanTier

		if refreshedKey.Email == "" {
			if email, emailErr := fetchAntigravityUserEmail(refreshCtx, refreshedKey.AccessToken, proxyURL); emailErr == nil && email != "" {
				refreshedKey.Email = email
			}
		}
		if refreshedKey.ProjectID == "" || refreshedKey.PlanTier == "" {
			if projID, tier, projErr := FetchAntigravityProjectAndTier(refreshCtx, refreshedKey.AccessToken, proxyURL); projErr == nil {
				if refreshedKey.ProjectID == "" {
					refreshedKey.ProjectID = projID
				}
				if refreshedKey.PlanTier == "" {
					refreshedKey.PlanTier = tier
				}
			}
		}

		oauthKey = refreshedKey
		return mergeAntigravityKeyJSON(current.Key, refreshedKey)
	})
	if err != nil {
		return nil, nil, err
	}
	return oauthKey, ch, nil
}

// EnsureAntigravityChannelAccessToken checks whether a single-key Antigravity
// subscription channel's OAuth token is expired or within the refresh lead
// window and refreshes it lazily before use.
func EnsureAntigravityChannelAccessToken(ctx context.Context, ch *model.Channel) (string, error) {
	if ch == nil {
		return "", fmt.Errorf("channel is nil")
	}
	parsed, ok := ParseAntigravityOAuthKey(ch.Key)
	if !ok {
		return ExtractAntigravityAccessToken(ch.Key)
	}
	if parsed.RefreshToken == "" || ch.Id <= 0 || ch.ChannelInfo.IsMultiKey {
		return parsed.AccessToken, nil
	}
	if parsed.AccessToken != "" && grokTokenUsable(parsed.Expired) {
		return parsed.AccessToken, nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	refreshed, updatedCh, err := refreshAntigravityChannelCredential(refreshCtx, ch, false)
	if err != nil {
		if parsed.AccessToken != "" {
			return parsed.AccessToken, nil
		}
		return "", err
	}
	if updatedCh != nil {
		ch.Key = updatedCh.Key
	}
	return refreshed.AccessToken, nil
}

func mergeAntigravityKeyJSON(oldKey string, refreshed *AntigravityOAuthKey) (string, error) {
	keyMap := make(map[string]any)
	trimmed := strings.TrimSpace(oldKey)
	if strings.HasPrefix(trimmed, "{") {
		_ = common.Unmarshal([]byte(trimmed), &keyMap)
	}
	keyMap["access_token"] = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		keyMap["refresh_token"] = refreshed.RefreshToken
	}
	if refreshed.ProjectID != "" {
		keyMap["project_id"] = refreshed.ProjectID
	}
	if refreshed.Email != "" {
		keyMap["email"] = refreshed.Email
	}
	if refreshed.PlanTier != "" {
		keyMap["plan_tier"] = refreshed.PlanTier
	}
	if refreshed.LastRefresh != "" {
		keyMap["last_refresh"] = refreshed.LastRefresh
	}
	if refreshed.Expired != "" {
		keyMap["expired"] = refreshed.Expired
	}
	out, err := common.Marshal(keyMap)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
