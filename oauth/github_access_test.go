package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

type githubTestTransport func(*http.Request) (*http.Response, error)

func (f githubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubAccessRequiresExplicitIDOrActiveMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   string
		statuses []int
		bodies   []string
		allowed  bool
	}{
		{"disabled", common.DefaultGitHubAccessPolicy, nil, nil, true},
		{"explicit permanent ID", `{"enabled":true,"user_ids":["42"],"organizations":["private-org"]}`, nil, nil, true},
		{"empty denies everyone", `{"enabled":true}`, nil, nil, false},
		{"unlisted ID", `{"enabled":true,"user_ids":["43"]}`, nil, nil, false},
		{"second organization active", `{"enabled":true,"organizations":["one","two"]}`, []int{404, 200}, []string{`{}`, `{"state":"active","organization":{"login":"TWO"}}`}, true},
		{"pending invitation", `{"enabled":true,"organizations":["one"]}`, []int{200}, []string{`{"state":"pending","organization":{"login":"one"}}`}, false},
		{"wrong organization", `{"enabled":true,"organizations":["one"]}`, []int{200}, []string{`{"state":"active","organization":{"login":"other"}}`}, false},
		{"missing state", `{"enabled":true,"organizations":["one"]}`, []int{200}, []string{`{"organization":{"login":"one"}}`}, false},
		{"malformed response", `{"enabled":true,"organizations":["one"]}`, []int{200}, []string{`invalid`}, false},
		{"approval denied", `{"enabled":true,"organizations":["one"]}`, []int{403}, []string{`{}`}, false},
		{"upstream unavailable", `{"enabled":true,"organizations":["one"]}`, []int{503}, []string{`{}`}, false},
		{"transport error", `{"enabled":true,"organizations":["one"]}`, []int{0}, []string{``}, false},
		{"another org permits despite error", `{"enabled":true,"organizations":["one","two"]}`, []int{403, 200}, []string{`{}`, `{"state":"active","organization":{"login":"two"}}`}, true},
		{"invalid stored policy", `invalid`, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous, transport := common.OptionMap, http.DefaultTransport
			common.OptionMap = map[string]string{common.GitHubAccessPolicyKey: tc.policy}
			t.Cleanup(func() { common.OptionMap = previous; http.DefaultTransport = transport })
			calls := 0
			http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
				require.Less(t, calls, len(tc.statuses))
				require.Equal(t, "api.github.com", r.URL.Host)
				require.True(t, strings.HasPrefix(r.URL.Path, "/user/memberships/orgs/"))
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				status, body := tc.statuses[calls], tc.bodies[calls]
				calls++
				if status == 0 {
					return nil, fmt.Errorf("unavailable")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			grant, err := (&GitHubProvider{}).AuthorizeUser(context.Background(), &OAuthToken{AccessToken: "test-token"}, "42")
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, "42", grant.UserID)
			} else {
				require.Error(t, err)
				require.Nil(t, grant)
			}
			require.Equal(t, len(tc.statuses), calls)
		})
	}
}

func TestGitHubUserInfoRejectsUnlistedAccountBeforeReturningIdentity(t *testing.T) {
	previous, transport := common.OptionMap, http.DefaultTransport
	common.OptionMap = map[string]string{common.GitHubAccessPolicyKey: `{"enabled":true,"user_ids":["43"]}`}
	t.Cleanup(func() { common.OptionMap = previous; http.DefaultTransport = transport })
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/user", r.URL.Path)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":42,"login":"allowed-looking-name"}`))}, nil
	})
	user, err := (&GitHubProvider{}).GetUserInfo(context.Background(), &OAuthToken{AccessToken: "test-token"})
	require.Error(t, err)
	require.Nil(t, user)
}
