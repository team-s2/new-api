package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchGrokBillingUsageMergesWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/billing", r.URL.Path)
		assert.Equal(t, "Bearer at", r.Header.Get("Authorization"))
		assert.Equal(t, GrokCLIVersion, r.Header.Get("x-grok-client-version"))
		assert.Equal(t, GrokCLIUserAgent(), r.Header.Get("User-Agent"))

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("format") == "credits" {
			_, _ = w.Write([]byte(`{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"},"creditUsagePercent":2.0,"productUsage":[{"product":"Api","usagePercent":2.0}],"prepaidBalance":{"val":12},"onDemandCap":{"val":100},"onDemandUsed":{"val":5},"isUnifiedBillingUser":true,"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"config":{"monthlyLimit":{"val":15000},"used":{"val":78},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
	}))
	defer server.Close()

	usage, status, err := FetchGrokBillingUsage(context.Background(), server.Client(), server.URL, "at")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	require.NotNil(t, usage)
	assert.Empty(t, usage.FailedWindows)
	assert.Equal(t, "SuperGrok", usage.Plan)

	require.NotNil(t, usage.Weekly)
	assert.InDelta(t, 2.0, *usage.Weekly.UsagePercent, 1e-9)
	assert.Equal(t, "2026-07-16T03:25:00Z", usage.Weekly.PeriodEnd, "weekly end must not inherit the monthly period")
	require.Len(t, usage.ProductUsage, 1)
	assert.Equal(t, "Api", usage.ProductUsage[0].Product)
	assert.InDelta(t, 12, *usage.PrepaidBalance, 1e-9)
	assert.InDelta(t, 100, *usage.OnDemandCap, 1e-9)
	assert.InDelta(t, 5, *usage.OnDemandUsed, 1e-9)
	assert.True(t, usage.IsUnifiedBillingUser)

	require.NotNil(t, usage.Monthly)
	assert.InDelta(t, 150, *usage.Monthly.MonthlyLimit, 1e-9, "monthly limit is reported in cents")
	assert.InDelta(t, 0.78, *usage.Monthly.MonthlyUsed, 1e-9)
	assert.InDelta(t, 0.52, *usage.Monthly.UsedPercent, 1e-9)
	assert.Equal(t, "2026-08-01T00:00:00Z", usage.Monthly.PeriodEnd)
}

func TestFetchGrokBillingUsagePartialAndFailedProbes(t *testing.T) {
	cases := []struct {
		name          string
		weeklyStatus  int
		monthlyStatus int
		wantStatus    int
		wantFailed    []string
		wantPlan      string
	}{
		{name: "weekly rate limited", weeklyStatus: http.StatusTooManyRequests, monthlyStatus: http.StatusOK, wantStatus: http.StatusOK, wantFailed: []string{"weekly"}, wantPlan: "SuperGrok Heavy"},
		{name: "both unauthorized", weeklyStatus: http.StatusUnauthorized, monthlyStatus: http.StatusUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "both forbidden", weeklyStatus: http.StatusForbidden, monthlyStatus: http.StatusForbidden, wantStatus: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("format") == "credits" {
					w.WriteHeader(tc.weeklyStatus)
					return
				}
				w.WriteHeader(tc.monthlyStatus)
				_, _ = w.Write([]byte(`{"config":{"monthlyLimit":150000,"used":"30000"}}`))
			}))
			defer server.Close()

			usage, status, err := FetchGrokBillingUsage(context.Background(), server.Client(), server.URL, "at")
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, status)
			if tc.wantStatus != http.StatusOK {
				assert.Nil(t, usage)
				return
			}
			require.NotNil(t, usage)
			assert.Equal(t, tc.wantFailed, usage.FailedWindows)
			assert.Equal(t, tc.wantPlan, usage.Plan)
			assert.Nil(t, usage.Weekly)
			require.NotNil(t, usage.Monthly)
			assert.InDelta(t, 20.0, *usage.Monthly.UsedPercent, 1e-9)
		})
	}
}
