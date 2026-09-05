package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildZhipuOAuthAuthorizeURL(t *testing.T) {
	state, err := GenerateZhipuOAuthState()
	require.NoError(t, err)
	require.Len(t, state, 32)

	authorizeURL := BuildZhipuOAuthAuthorizeURL(state)
	parsed, err := url.Parse(authorizeURL)
	require.NoError(t, err)
	require.Equal(t, "bigmodel.cn", parsed.Host)
	require.Equal(t, "/login", parsed.Path)

	query := parsed.Query()
	require.Equal(t, "zcode", query.Get("appId"))
	require.Equal(t, state, query.Get("state"))

	relay, err := url.Parse(query.Get("redirect"))
	require.NoError(t, err)
	require.Equal(t, "zcode.z.ai", relay.Host)
	require.Equal(t, "/app/oauth/login", relay.Path)
	require.Equal(t, zhipuOAuthCallbackURI, relay.Query().Get("redirect"))
}

func TestParseZhipuOAuthCallback(t *testing.T) {
	const state = "state-1234"

	cases := []struct {
		name      string
		input     string
		wantCode  string
		wantError string
	}{
		{name: "full callback with authCode", input: "zcode://oauth/callback?authCode=abc&state=" + state, wantCode: "abc"},
		{name: "full callback with code", input: "zcode://oauth/callback?code=def&state=" + state, wantCode: "def"},
		{name: "bare code", input: "ghi", wantCode: "ghi"},
		{name: "state mismatch", input: "zcode://oauth/callback?code=def&state=other", wantError: "state mismatch"},
		{name: "missing code", input: "zcode://oauth/callback?state=" + state, wantError: "authorization code"},
		{name: "empty", input: "  ", wantError: "empty"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, err := ParseZhipuOAuthCallback(testCase.input, state)
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, testCase.wantCode, code)
		})
	}
}

func TestExchangeZhipuOAuthCodeParsesTokenSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/", request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, common.Unmarshal(body, &payload))
		assert.Equal(t, "bigmodel", payload["provider"])
		assert.Equal(t, "the-code", payload["code"])
		assert.Equal(t, zhipuOAuthCallbackURI, payload["redirect_uri"])
		assert.Equal(t, "state-1", payload["state"])

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":200,"msg":"","data":{"token":"zcode-jwt","bigmodel":{"access_token":"bm-access","refresh_token":"bm-refresh"},"user":{"customerName":"Alice","customerNumber":"c-1"}}}`))
	}))
	defer server.Close()
	original := zhipuOAuthTokenURL
	zhipuOAuthTokenURL = server.URL
	defer func() { zhipuOAuthTokenURL = original }()

	tokenSet, err := ExchangeZhipuOAuthCode(context.Background(), server.Client(), "the-code", "state-1")
	require.NoError(t, err)
	assert.Equal(t, "zcode-jwt", tokenSet.ZcodeJWT)
	assert.Equal(t, "bm-access", tokenSet.AccessToken)
	assert.Equal(t, "bm-refresh", tokenSet.RefreshToken)
	assert.Equal(t, "Alice", tokenSet.Username)
}

func TestExchangeZhipuOAuthCodeAcceptsCamelCaseTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"data":{"bigmodel":{"accessToken":"bm-access","refreshToken":"bm-refresh"}}}`))
	}))
	defer server.Close()
	original := zhipuOAuthTokenURL
	zhipuOAuthTokenURL = server.URL
	defer func() { zhipuOAuthTokenURL = original }()

	tokenSet, err := ExchangeZhipuOAuthCode(context.Background(), server.Client(), "code", "state")
	require.NoError(t, err)
	assert.Equal(t, "bm-access", tokenSet.AccessToken)
	assert.Equal(t, "bm-refresh", tokenSet.RefreshToken)
}

func TestExchangeZhipuOAuthCodeRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"code":2007,"msg":"http error"}`))
	}))
	defer server.Close()
	original := zhipuOAuthTokenURL
	zhipuOAuthTokenURL = server.URL
	defer func() { zhipuOAuthTokenURL = original }()

	_, err := ExchangeZhipuOAuthCode(context.Background(), server.Client(), "bad", "state")
	require.ErrorContains(t, err, "http error")
}

func TestDeriveZhipuCodingPlanAPIKeyReusesExistingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "bm-access", request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/api/biz/customer/getCustomerInfo":
			_, _ = writer.Write([]byte(`{"code":200,"data":{"customerName":"Alice","organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys":
			_, _ = writer.Write([]byte(`{"code":200,"data":[{"name":"other-key","apiKey":"other"},{"name":"zcode-api-key","apiKey":"key-id"}]}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys/copy/key-id":
			_, _ = writer.Write([]byte(`{"code":200,"data":{"secretKey":"secret"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	original := zhipuConsoleBaseURL
	zhipuConsoleBaseURL = server.URL
	defer func() { zhipuConsoleBaseURL = original }()

	apiKey, info, err := DeriveZhipuCodingPlanAPIKey(context.Background(), server.Client(), "bm-access")
	require.NoError(t, err)
	assert.Equal(t, "key-id.secret", apiKey)
	require.NotNil(t, info)
	assert.Equal(t, "Alice", info.CustomerName)
}

func TestDeriveZhipuCodingPlanAPIKeyCreatesMissingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/biz/customer/getCustomerInfo":
			_, _ = writer.Write([]byte(`{"code":200,"data":{"organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys":
			if request.Method == http.MethodPost {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), `"zcode-api-key"`)
				_, _ = writer.Write([]byte(`{"code":200,"data":{"name":"zcode-api-key","apiKey":"created-id"}}`))
				return
			}
			_, _ = writer.Write([]byte(`{"code":200,"data":[]}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys/copy/created-id":
			_, _ = writer.Write([]byte(`{"code":200,"data":{"secretKey":"created-secret"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	original := zhipuConsoleBaseURL
	zhipuConsoleBaseURL = server.URL
	defer func() { zhipuConsoleBaseURL = original }()

	apiKey, _, err := DeriveZhipuCodingPlanAPIKey(context.Background(), server.Client(), "bm-access")
	require.NoError(t, err)
	assert.Equal(t, "created-id.created-secret", apiKey)
}

func TestDeriveZhipuCodingPlanAPIKeyRequiresDefaultProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"code":200,"data":{"organizations":[]}}`))
	}))
	defer server.Close()
	original := zhipuConsoleBaseURL
	zhipuConsoleBaseURL = server.URL
	defer func() { zhipuConsoleBaseURL = original }()

	_, _, err := DeriveZhipuCodingPlanAPIKey(context.Background(), server.Client(), "bm-access")
	require.ErrorContains(t, err, "default organization or project not found")
}

func TestDeriveZhipuCodingPlanAPIKeyRejectsMissingSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/biz/customer/getCustomerInfo":
			_, _ = writer.Write([]byte(`{"code":200,"data":{"organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys":
			_, _ = writer.Write([]byte(`{"code":200,"data":[{"name":"zcode-api-key","apiKey":"key-id"}]}`))
		case "/api/biz/v1/organization/org-default/projects/project-default/api_keys/copy/key-id":
			_, _ = writer.Write([]byte(`{"code":200,"data":{}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	original := zhipuConsoleBaseURL
	zhipuConsoleBaseURL = server.URL
	defer func() { zhipuConsoleBaseURL = original }()

	_, _, err := DeriveZhipuCodingPlanAPIKey(context.Background(), server.Client(), "bm-access")
	require.ErrorContains(t, err, "missing secretKey")
}

