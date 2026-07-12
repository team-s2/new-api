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
