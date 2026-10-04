package groksub

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// OAuthKey is the JSON credential stored in the Grok Subscription channel key.
// The access token is short-lived; RefreshToken drives the auto-refresh task
// and the lazy request-path refresh.
type OAuthKey struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`

	Email       string `json:"email,omitempty"`
	Subject     string `json:"sub,omitempty"`
	PlanTier    string `json:"plan_tier,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	Expired     string `json:"expired,omitempty"`
}

func ParseOAuthKey(raw string) (*OAuthKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("grok subscription channel: empty oauth key")
	}
	var key OAuthKey
	if err := common.Unmarshal([]byte(raw), &key); err != nil {
		return nil, errors.New("grok subscription channel: invalid oauth key json")
	}
	return &key, nil
}
