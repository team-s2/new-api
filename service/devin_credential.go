package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// DevinCredential is the JSON stored in a Devin Subscription channel key.
// `session_token` is minted by the PKCE OAuth flow; `api_key` is an optional
// durable cog_* key that can mint fresh session tokens for self-healing.
type DevinCredential struct {
	SessionToken string `json:"session_token,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	UserName     string `json:"user_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	OrgID        string `json:"org_id,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// AccessToken returns the bearer token used for upstream calls: the durable
// api_key when present (the upstream accepts cog_* keys directly), otherwise
// the OAuth session token.
func (c *DevinCredential) AccessToken() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return c.SessionToken
}

func ParseDevinCredential(raw string) (*DevinCredential, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("devin subscription channel: empty credential")
	}
	var cred DevinCredential
	if err := common.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, errors.New("devin subscription channel: invalid credential json")
	}
	if cred.AccessToken() == "" {
		return nil, errors.New("devin subscription channel: credential needs session_token or api_key")
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
		return "", errors.New("devin subscription channel: empty api_key")
	}
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

// EnsureDevinChannelSessionToken re-mints the session token from the durable
// api_key when the channel has one but no session token (or after upstream
// auth failure invalidation). Returns the key JSON the relay should use.
func EnsureDevinChannelSessionToken(ctx context.Context, channel *model.Channel) (string, error) {
	cred, err := ParseDevinCredential(channel.Key)
	if err != nil {
		return "", err
	}
	if cred.SessionToken != "" || cred.APIKey == "" {
		return channel.Key, nil
	}
	token, err := MintDevinSessionToken(ctx, cred.APIKey, channel.GetBaseURL(), channel.GetSetting().Proxy)
	if err != nil {
		return "", err
	}
	cred.SessionToken = token
	encoded, err := common.Marshal(cred)
	if err != nil {
		return "", err
	}
	updated, err := model.UpdateChannelCredential(ctx, channel.Id, func(current *model.Channel) (string, error) {
		currentCred, parseErr := ParseDevinCredential(current.Key)
		if parseErr != nil {
			return "", parseErr
		}
		if currentCred.SessionToken != "" {
			// Another writer already refreshed it; keep theirs.
			return current.Key, nil
		}
		currentCred.SessionToken = token
		currentCred.CreatedAt = time.Now().Format(time.RFC3339)
		merged, marshalErr := common.Marshal(currentCred)
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(merged), nil
	})
	if err != nil {
		return "", fmt.Errorf("devin subscription channel: persist session token: %w", err)
	}
	if updated != nil && updated.Key != "" {
		return updated.Key, nil
	}
	return string(encoded), nil
}
