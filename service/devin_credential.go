package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// DevinCredential is the JSON stored in a Devin Subscription channel key.
// `session_token` is minted by the PKCE OAuth flow; `api_key` is an optional
// durable cog_* key that can be used directly or manually mint a session token.
type DevinCredential struct {
	SessionToken string `json:"session_token,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	UserName     string `json:"user_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	OrgID        string `json:"org_id,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// AccessTokens lists the upstream tokens in the order callers should try
// them. The explicitly acquired session token comes first, so OAuth login and
// manual refresh affect subsequent calls; the durable api_key follows, so an
// expired session token does not take down a channel that still has one.
func (c *DevinCredential) AccessTokens() []string {
	tokens := make([]string, 0, 2)
	if c.SessionToken != "" {
		tokens = append(tokens, c.SessionToken)
	}
	if c.APIKey != "" && c.APIKey != c.SessionToken {
		tokens = append(tokens, c.APIKey)
	}
	return tokens
}

func ParseDevinCredential(raw string) (*DevinCredential, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, common.NewMessage("Devin credential is empty")
	}
	var cred DevinCredential
	if err := common.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, common.NewMessage("Invalid Devin credential JSON")
	}
	cred.APIKey = strings.TrimSpace(cred.APIKey)
	cred.SessionToken = strings.TrimSpace(cred.SessionToken)
	if len(cred.AccessTokens()) == 0 {
		return nil, common.NewMessage("Devin credential requires a session token or API key")
	}
	return &cred, nil
}

// NormalizeDevinSessionToken ensures the devin-session-token$ prefix the
// upstream metadata expects.
func NormalizeDevinSessionToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "devin-session-token$") {
		return token
	}
	return "devin-session-token$" + token
}

// MintDevinSessionToken exchanges a durable cog_* API key for a session token
// via SeatManagementService/GetSelfDevinSessionToken. The RPC schema is not
// public; the request carries the key in the same metadata slot a session
// token would occupy and we scan the response for the token prefix.
func MintDevinSessionToken(ctx context.Context, apiKey, baseURL, proxyURL string) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", common.NewMessage("Devin credential requires an API key for manual refresh")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	payload := buildDevinMetadataRequest(apiKey)
	body, err := postDevinConnectRPC(ctx, devinSeatTokenRPCPath(baseURL), apiKey, proxyURL, payload)
	if err != nil {
		return "", err
	}
	token := extractDevinSessionToken(body)
	if token == "" {
		return "", errors.New("devin subscription channel: session token mint response missing token")
	}
	return token, nil
}

// RefreshDevinChannelSessionToken mints a new session token even when the
// observed credential already has one, without overwriting a concurrent refresh.
func RefreshDevinChannelSessionToken(ctx context.Context, channel *model.Channel) (string, error) {
	if channel == nil || channel.Type != constant.ChannelTypeDevinSub || channel.ChannelInfo.IsMultiKey {
		return "", common.NewMessage("Only single-key Devin Subscription channels are supported")
	}
	cred, err := ParseDevinCredential(channel.Key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cred.APIKey) == "" {
		return "", common.NewMessage("Devin credential requires an API key for manual refresh")
	}
	token, err := MintDevinSessionToken(ctx, cred.APIKey, channel.GetBaseURL(), channel.GetSetting().Proxy)
	if err != nil {
		return "", err
	}
	updated, err := model.UpdateChannelCredential(ctx, channel.Id, func(current *model.Channel) (string, error) {
		if current.Type != constant.ChannelTypeDevinSub || current.ChannelInfo.IsMultiKey {
			return "", common.NewMessage("Only single-key Devin Subscription channels are supported")
		}
		if current.Key != channel.Key || current.GetBaseURL() != channel.GetBaseURL() || current.GetSetting().Proxy != channel.GetSetting().Proxy {
			return "", common.NewMessage("Devin channel configuration changed during refresh. Please retry")
		}
		cred.SessionToken = token
		cred.CreatedAt = time.Now().Format(time.RFC3339)
		merged, marshalErr := common.Marshal(cred)
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(merged), nil
	})
	if err != nil {
		return "", fmt.Errorf("devin subscription channel: persist session token: %w", err)
	}
	return updated.Key, nil
}
