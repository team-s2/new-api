package oauth

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
)

// AuthorizeUser checks the authenticated account, not browser-supplied names.
// Explicit IDs and active organization memberships are alternatives (OR).
func (p *GitHubProvider) AuthorizeUser(ctx context.Context, token *OAuthToken, userID string) (*common.GitHubLoginGrant, error) {
	policy, raw, err := common.CurrentGitHubAccessPolicy()
	if err != nil {
		return nil, NewOAuthError(i18n.MsgGitHubAccessCheckFailed, nil)
	}
	grant := &common.GitHubLoginGrant{UserID: userID, Policy: raw}
	if !policy.Enabled || slices.Contains(policy.UserIDs, userID) {
		return grant, nil
	}
	// Bound the whole membership check, including multiple organizations.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	checkFailed := false
	for _, org := range policy.Organizations {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/memberships/orgs/"+org, nil)
		if err != nil {
			return nil, NewOAuthError(i18n.MsgGitHubAccessCheckFailed, nil)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		res, err := client.Do(req)
		if err != nil {
			checkFailed = true
			continue
		}
		var membership struct {
			State        string `json:"state"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		if res.StatusCode == http.StatusOK {
			err = common.DecodeJson(res.Body, &membership)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			continue
		}
		if res.StatusCode != http.StatusOK || err != nil || !strings.EqualFold(membership.Organization.Login, org) {
			checkFailed = true
			continue
		}
		if membership.State == "active" {
			return grant, nil
		}
	}
	// Do not log GitHub responses or access tokens.
	logger.LogWarn(ctx, "GitHub OAuth access denied for account ID "+userID)
	if checkFailed {
		return nil, NewOAuthError(i18n.MsgGitHubAccessCheckFailed, nil)
	}
	return nil, NewOAuthError(i18n.MsgGitHubAccessDenied, nil)
}
