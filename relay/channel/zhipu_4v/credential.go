package zhipu_4v

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type CodingPlanCredential struct {
	APIKey          string `json:"api_key"`
	AccountUsername string `json:"account_username,omitempty"`
	AccountPassword string `json:"account_password,omitempty"`
}

func ParseCodingPlanCredential(raw string) (*CodingPlanCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("zhipu coding plan: empty credential")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return &CodingPlanCredential{APIKey: trimmed}, nil
	}

	var credential CodingPlanCredential
	if err := common.Unmarshal([]byte(trimmed), &credential); err != nil {
		return nil, errors.New("zhipu coding plan: invalid credential json")
	}
	credential.APIKey = strings.TrimSpace(credential.APIKey)
	credential.AccountUsername = strings.TrimSpace(credential.AccountUsername)
	if credential.APIKey == "" {
		return nil, errors.New("zhipu coding plan: api_key is required")
	}
	return &credential, nil
}

// OAuthCredential is the key format of the BigModel Subscription (Coding Plan)
// channel. It is produced by the OAuth login helper: api_key is the coding
// plan API key derived from the OAuth access token, and access_token is the
// bigmodel.cn console token used to query subscription usage.
//
// refresh_token is stored but never consumed: the upstream does not expose a
// refresh endpoint (the official ZCode client shows "refresh token 交换接口
// 未提供" and asks users to log in again), so an expired access_token only
// disables usage queries until the channel owner re-runs OAuth login.
type OAuthCredential struct {
	APIKey       string `json:"api_key"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	OAuthUser    string `json:"oauth_username,omitempty"`
}

func ParseOAuthCredential(raw string) (*OAuthCredential, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("bigmodel subscription: empty credential")
	}
	var credential OAuthCredential
	if err := common.Unmarshal([]byte(trimmed), &credential); err != nil {
		return nil, errors.New("bigmodel subscription: credential must be the JSON produced by OAuth login")
	}
	credential.APIKey = strings.TrimSpace(credential.APIKey)
	credential.AccessToken = strings.TrimSpace(credential.AccessToken)
	credential.RefreshToken = strings.TrimSpace(credential.RefreshToken)
	credential.OAuthUser = strings.TrimSpace(credential.OAuthUser)
	if credential.APIKey == "" {
		return nil, errors.New("bigmodel subscription: credential is missing api_key, please run OAuth login again")
	}
	if credential.AccessToken == "" {
		return nil, errors.New("bigmodel subscription: credential is missing access_token, please run OAuth login again")
	}
	return &credential, nil
}
