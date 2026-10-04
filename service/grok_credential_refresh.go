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

// RefreshGrokChannelCredential forces a refresh unless a concurrent caller has
// already replaced the credential that this call observed. The updated channel
// is always published to the local cache after the transaction commits.
func RefreshGrokChannelCredential(ctx context.Context, channelID int) (*GrokOAuthKey, *model.Channel, error) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, nil, err
	}
	return refreshGrokChannelCredential(ctx, ch, true)
}

func refreshGrokChannelCredential(ctx context.Context, observed *model.Channel, force bool) (*GrokOAuthKey, *model.Channel, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var oauthKey *GrokOAuthKey
	ch, err := model.UpdateChannelCredential(refreshCtx, observed.Id, func(current *model.Channel) (string, error) {
		if current.Type != constant.ChannelTypeGrokSub || current.ChannelInfo.IsMultiKey {
			return "", fmt.Errorf("channel must be a single-key Grok Subscription channel")
		}
		var err error
		oauthKey, err = ParseGrokOAuthKey(current.Key)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(oauthKey.AccessToken) != "" && (current.Key != observed.Key || (!force && grokTokenUsable(oauthKey.Expired))) {
			return current.Key, nil
		}
		if strings.TrimSpace(oauthKey.RefreshToken) == "" {
			return "", fmt.Errorf("grok subscription channel: refresh_token is required to refresh credential")
		}
		res, err := RefreshGrokOAuthToken(refreshCtx, oauthKey.RefreshToken, current.GetSetting().Proxy)
		if err != nil {
			return "", err
		}
		oauthKey.AccessToken = strings.TrimSpace(res.AccessToken)
		oauthKey.RefreshToken = strings.TrimSpace(res.RefreshToken)
		if strings.TrimSpace(res.IDToken) != "" {
			oauthKey.IDToken = strings.TrimSpace(res.IDToken)
		}
		oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
		oauthKey.Expired = ""
		if res.ExpiresIn > 0 {
			oauthKey.Expired = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second).Format(time.RFC3339)
		}
		encoded, err := common.Marshal(oauthKey)
		return string(encoded), err
	})
	if err != nil {
		return nil, nil, err
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
	refreshed, _, err := refreshGrokChannelCredential(refreshCtx, ch, false)
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
