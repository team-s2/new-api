package common

import (
	"fmt"
	"regexp"
	"strconv"
)

const GitHubAccessPolicyKey = "GitHubAccessPolicy"
const DefaultGitHubAccessPolicy = `{"enabled":false,"organizations":[],"user_ids":[],"role":0}`

// GitHubAccessPolicy is saved as one option so access and role changes are atomic.
type GitHubAccessPolicy struct {
	Enabled       bool     `json:"enabled"`
	Organizations []string `json:"organizations"`
	UserIDs       []string `json:"user_ids"`
	Role          int      `json:"role"`
}

var githubOrganizationName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

func ParseGitHubAccessPolicy(raw string) (GitHubAccessPolicy, error) {
	var decoded struct {
		GitHubAccessPolicy
		Enabled *bool `json:"enabled"`
	}
	err := UnmarshalJsonStr(raw, &decoded)
	policy := decoded.GitHubAccessPolicy
	if err != nil || decoded.Enabled == nil {
		return policy, fmt.Errorf("invalid GitHub access policy")
	}
	policy.Enabled = *decoded.Enabled
	if policy.Role != 0 && policy.Role != RoleAdminUser && policy.Role != RoleRootUser {
		return policy, fmt.Errorf("invalid GitHub automatic role")
	}
	if !policy.Enabled && policy.Role != 0 {
		return policy, fmt.Errorf("GitHub automatic roles require the allowlist")
	}
	if len(policy.Organizations) > 100 || len(policy.UserIDs) > 1000 {
		return policy, fmt.Errorf("GitHub allowlist is too large")
	}
	for _, org := range policy.Organizations {
		if !githubOrganizationName.MatchString(org) {
			return policy, fmt.Errorf("invalid GitHub organization name: %q", org)
		}
	}
	for _, id := range policy.UserIDs {
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != id {
			return policy, fmt.Errorf("GitHub users must be positive numeric account IDs")
		}
	}
	return policy, nil
}

func CurrentGitHubAccessPolicy() (GitHubAccessPolicy, string, error) {
	OptionMapRWMutex.RLock()
	raw, ok := OptionMap[GitHubAccessPolicyKey]
	OptionMapRWMutex.RUnlock()
	if !ok {
		raw = DefaultGitHubAccessPolicy
	}
	policy, err := ParseGitHubAccessPolicy(raw)
	return policy, raw, err
}

// GitHubLoginGrant carries only server-verified identity and policy, never tokens.
// It remains comparable so MFA completion can compare the exact bound grant.
type GitHubLoginGrant struct {
	UserID string `json:"user_id"`
	Policy string `json:"policy"`
}
