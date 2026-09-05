package zhipu_4v

import (
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCodingPlanCredentialSupportsLegacyAndAccountCredentials(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected *CodingPlanCredential
	}{
		{
			name: "legacy api key",
			raw:  "legacy-key",
			expected: &CodingPlanCredential{
				APIKey: "legacy-key",
			},
		},
		{
			name: "account credential",
			raw:  `{"api_key":"coding-key","account_username":"user","account_password":"password"}`,
			expected: &CodingPlanCredential{
				APIKey:          "coding-key",
				AccountUsername: "user",
				AccountPassword: "password",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credential, err := ParseCodingPlanCredential(test.raw)
			require.NoError(t, err)
			assert.Equal(t, test.expected, credential)
		})
	}
}

func TestSetupRequestHeaderUsesOnlyCodingPlanAPIKey(t *testing.T) {
	adaptor := &Adaptor{}
	headers := http.Header{}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey:         `{"api_key":"coding-key","account_username":"user","account_password":"password"}`,
			ChannelBaseUrl: "glm-coding-plan",
		},
	}

	err := adaptor.SetupRequestHeader(context, &headers, info)
	require.NoError(t, err)
	assert.Equal(t, "Bearer coding-key", headers.Get("Authorization"))
	assert.NotContains(t, headers.Get("Authorization"), "password")
}

func TestParseOAuthCredential(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		expected  *OAuthCredential
		wantError string
	}{
		{
			name: "full credential",
			raw:  `{"api_key":"id.secret","access_token":"token","refresh_token":"refresh","oauth_username":"Alice"}`,
			expected: &OAuthCredential{APIKey: "id.secret", AccessToken: "token", RefreshToken: "refresh", OAuthUser: "Alice"},
		},
		{
			name:      "without refresh token",
			raw:       `{"api_key":"id.secret","access_token":"token"}`,
			expected:  &OAuthCredential{APIKey: "id.secret", AccessToken: "token"},
		},
		{
			name:      "missing api_key",
			raw:       `{"access_token":"token"}`,
			wantError: "missing api_key",
		},
		{
			name:      "missing access_token",
			raw:       `{"api_key":"id.secret"}`,
			wantError: "missing access_token",
		},
		{
			name:      "not json",
			raw:       "id.secret",
			wantError: "JSON produced by OAuth login",
		},
		{
			name:      "empty",
			raw:       "  ",
			wantError: "empty credential",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			credential, err := ParseOAuthCredential(testCase.raw)
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, testCase.expected, credential)
		})
	}
}
