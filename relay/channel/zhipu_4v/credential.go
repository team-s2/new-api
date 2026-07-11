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
