package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchZhipuCodingPlanUsageLogsInAndNormalizesQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/auth/login":
			var payload map[string]string
			require.NoError(t, common.DecodeJson(request.Body, &payload))
			assert.Equal(t, "user", payload["username"])
			assert.Equal(t, "password", payload["password"])
			_, _ = writer.Write([]byte(`{"success":true,"data":{"access_token":"account-token"}}`))
		case "/api/biz/customer/getCustomerInfo":
			assert.Equal(t, "account-token", request.Header.Get("Authorization"))
			_, _ = writer.Write([]byte(`{"success":true,"data":{"organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/monitor/usage/quota/limit":
			assert.Equal(t, "account-token", request.Header.Get("Authorization"))
			assert.Equal(t, "org-default", request.Header.Get("Bigmodel-Organization"))
			assert.Equal(t, "project-default", request.Header.Get("Bigmodel-Project"))
			_, _ = writer.Write([]byte(`{"success":true,"data":{"level":"max","limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":14,"nextResetTime":1000},{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":53,"nextResetTime":2000},{"type":"TIME_LIMIT","unit":5,"number":1,"usage":4000,"currentValue":77,"remaining":3923,"percentage":1,"nextResetTime":3000,"usageDetails":[{"modelCode":"search-prime","usage":42}]},{"type":"UNKNOWN","unit":1,"number":1,"percentage":99}]}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	usage, err := fetchZhipuCodingPlanUsage(context.Background(), server.Client(), server.URL, "user", "password")
	require.NoError(t, err)
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.Weekly)
	require.NotNil(t, usage.MCPMonthly)
	assert.Equal(t, "max", usage.Level)
	assert.Equal(t, "prompts", usage.FiveHour.Unit)
	assert.Equal(t, "prompts", usage.Weekly.Unit)
	assert.Empty(t, usage.MCPMonthly.Unit)
	assert.Equal(t, 14, usage.FiveHour.Percentage)
	assert.Equal(t, int64(2000), usage.Weekly.NextResetTime)
	assert.Equal(t, int64(4000), *usage.MCPMonthly.Usage)
	assert.Equal(t, int64(77), *usage.MCPMonthly.CurrentValue)
	assert.Equal(t, []ZhipuCodingPlanUsageDetail{{ModelCode: "search-prime", Usage: 42}}, usage.MCPMonthly.UsageDetails)
}

func TestFetchZhipuCodingPlanUsageRejectsEmptyLoginResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	defer server.Close()

	_, err := fetchZhipuCodingPlanUsage(context.Background(), server.Client(), server.URL, "user", "password")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty upstream response")
}

func TestFetchZhipuCodingPlanUsageWithTokenQueriesWithoutLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/auth/login":
			t.Fatalf("token-based usage fetch must not log in, got %s", request.URL.Path)
		case "/api/biz/customer/getCustomerInfo":
			assert.Equal(t, "oauth-token", request.Header.Get("Authorization"))
			_, _ = writer.Write([]byte(`{"success":true,"data":{"organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/monitor/usage/quota/limit":
			_, _ = writer.Write([]byte(`{"success":true,"data":{"level":"max","limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":10}]}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	usage, err := fetchZhipuCodingPlanUsageWithToken(context.Background(), server.Client(), server.URL, "oauth-token")
	require.NoError(t, err)
	require.NotNil(t, usage.FiveHour)
	assert.Equal(t, 10, usage.FiveHour.Percentage)
	assert.Equal(t, "prompts", usage.FiveHour.Unit)
}

// Credit-based plans (sold from 2026-07-30) report the 5-hour and weekly
// windows as CREDIT_LIMIT and carry no monthly MCP window. The payload below
// is captured from a real lite account.
func TestFetchZhipuCodingPlanUsageWithTokenMapsCreditPlanLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/biz/customer/getCustomerInfo":
			_, _ = writer.Write([]byte(`{"success":true,"data":{"organizations":[{"organizationId":"org-default","isDefault":true,"projects":[{"projectId":"project-default","isDefault":true}]}]}}`))
		case "/api/monitor/usage/quota/limit":
			_, _ = writer.Write([]byte(`{"success":true,"data":{"level":"lite","limits":[` +
				`{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":69,"remaining":1930,"percentage":3,"nextResetTime":1790851347795},` +
				`{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":10000,"currentValue":73,"remaining":9926,"percentage":1,"nextResetTime":1791001173997}]}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	usage, err := fetchZhipuCodingPlanUsageWithToken(context.Background(), server.Client(), server.URL, "oauth-token")
	require.NoError(t, err)
	assert.Equal(t, "lite", usage.Level)
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.Weekly)
	assert.Nil(t, usage.MCPMonthly)
	assert.Equal(t, "credits", usage.FiveHour.Unit)
	assert.Equal(t, int64(2000), *usage.FiveHour.Usage)
	assert.Equal(t, int64(69), *usage.FiveHour.CurrentValue)
	assert.Equal(t, int64(1930), *usage.FiveHour.Remaining)
	assert.Equal(t, 3, usage.FiveHour.Percentage)
	assert.Equal(t, int64(1790851347795), usage.FiveHour.NextResetTime)
	assert.Equal(t, "credits", usage.Weekly.Unit)
	assert.Equal(t, int64(10000), *usage.Weekly.Usage)
	assert.Equal(t, int64(73), *usage.Weekly.CurrentValue)
	assert.Equal(t, 1, usage.Weekly.Percentage)
}

func TestFetchZhipuCodingPlanUsageWithTokenReportsExpiredOAuthToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchZhipuCodingPlanUsageWithToken(context.Background(), server.Client(), server.URL, "stale-token")
	require.ErrorIs(t, err, ErrZhipuOAuthTokenExpired)
}
