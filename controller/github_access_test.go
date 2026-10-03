package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type githubCallbackTransport func(*http.Request) (*http.Response, error)

func (f githubCallbackTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubCallbackDeniesUnlistedNewAndExistingUsers(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			db := setupManageUserTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
			previous, enabled, transport := common.OptionMap, common.GitHubOAuthEnabled, http.DefaultTransport
			common.OptionMap = map[string]string{common.GitHubAccessPolicyKey: `{"enabled":true,"user_ids":["43"],"role":100}`}
			common.GitHubOAuthEnabled = true
			t.Cleanup(func() {
				common.OptionMap = previous
				common.GitHubOAuthEnabled = enabled
				http.DefaultTransport = transport
			})
			if existing {
				require.NoError(t, db.Create(&model.User{Username: "member", GitHubId: "42", Role: 100, Status: common.UserStatusEnabled, AuthVersion: 1}).Error)
			}
			var before int64
			require.NoError(t, db.Model(&model.User{}).Count(&before).Error)
			calls := 0
			http.DefaultTransport = githubCallbackTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body := `{"access_token":"test-token","scope":"user:email read:org"}`
				if r.URL.Path == "/user" {
					body = `{"id":42,"login":"member"}`
				} else {
					require.Equal(t, "/login/oauth/access_token", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: model.AuthFlowPurposeOAuth, Provider: "github", Intent: model.AuthFlowIntentLogin, Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute)})
			require.NoError(t, err)
			router := gin.New()
			router.GET("/api/oauth/:provider", HandleOAuth)
			invalid := httptest.NewRecorder()
			router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/oauth/github?state=invalid&code=test", nil))
			require.Equal(t, http.StatusForbidden, invalid.Code)
			require.Zero(t, calls, "invalid state must not reach GitHub")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/oauth/github?state="+state+"&code=test", nil))
			require.Equal(t, http.StatusForbidden, response.Code)
			require.NotContains(t, response.Body.String(), "access_token")
			require.Empty(t, response.Result().Cookies())
			var after, sessions int64
			require.NoError(t, db.Model(&model.User{}).Count(&after).Error)
			require.NoError(t, db.Model(&model.UserSession{}).Count(&sessions).Error)
			require.Equal(t, before, after)
			require.Zero(t, sessions)
		})
	}
}
