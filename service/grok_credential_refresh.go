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

// RefreshGrokChannelCredential refreshes a Grok Subscription channel's OAuth
// tokens in place (channels.key) and returns the updated key.
func RefreshGrokChannelCredential(ctx context.Context, channelID int, resetCaches bool) (*GrokOAuthKey, *model.Channel, error) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	if ch == nil {
		return nil, nil, fmt.Errorf("channel not found")
	}
	if ch.Type != constant.ChannelTypeGrokSub {
		return nil, nil, fmt.Errorf("channel type is not Grok Subscription")
	}

	oauthKey, err := ParseGrokOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		return nil, nil, fmt.Errorf("grok subscription channel: refresh_token is required to refresh credential")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	res, err := RefreshGrokOAuthToken(refreshCtx, oauthKey.RefreshToken, ch.GetSetting().Proxy)
	if err != nil {
		return nil, nil, err
	}

	oauthKey.AccessToken = strings.TrimSpace(res.AccessToken)
	oauthKey.RefreshToken = strings.TrimSpace(res.RefreshToken)
	oauthKey.IDToken = strings.TrimSpace(res.IDToken)
	oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
	if res.ExpiresIn > 0 {
		oauthKey.Expired = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	encoded, err := common.Marshal(oauthKey)
	if err != nil {
		return nil, nil, err
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("key", string(encoded)).Error; err != nil {
		return nil, nil, err
	}
	if resetCaches {
		model.InitChannelCache()
	}
	return oauthKey, ch, nil
}

// EnsureGrokChannelAccessToken returns the channel key JSON with a usable
// access token, lazily refreshing when it is missing or within the refresh
// skew. It powers the relay request path so short-lived xAI tokens stay valid
// between the background task's ticks. On refresh failure the original key is
// returned unchanged so the relay can still try the stale token.
func EnsureGrokChannelAccessToken(ctx context.Context, ch *model.Channel) (string, error) {
	rawKey := strings.TrimSpace(ch.Key)
	oauthKey, err := ParseGrokOAuthKey(rawKey)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(oauthKey.AccessToken) != "" && grokTokenUsable(oauthKey.Expired) {
		return rawKey, nil
	}
	if strings.TrimSpace(oauthKey.RefreshToken) == "" {
		if strings.TrimSpace(oauthKey.AccessToken) != "" {
			return rawKey, nil
		}
		return "", fmt.Errorf("grok subscription channel: no usable credential")
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	refreshed, _, err := RefreshGrokChannelCredential(refreshCtx, ch.Id, false)
	if err != nil {
		if strings.TrimSpace(oauthKey.AccessToken) != "" {
			return rawKey, nil
		}
		return "", err
	}
	encoded, err := common.Marshal(refreshed)
	if err != nil {
		return rawKey, nil
	}
	return string(encoded), nil
}

// grokTokenUsable reports whether the recorded expiry is still ahead of the
// 5-minute refresh skew.
func grokTokenUsable(expiredRaw string) bool {
	expiredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(expiredRaw))
	if err != nil || expiredAt.IsZero() {
		return false
	}
	return time.Until(expiredAt) > 5*time.Minute
}
