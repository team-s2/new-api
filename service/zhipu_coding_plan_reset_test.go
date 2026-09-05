package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withZhipuResetTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	original := zhipuResetBaseURL
	zhipuResetBaseURL = server.URL
	t.Cleanup(func() {
		zhipuResetBaseURL = original
		server.Close()
	})
	return server
}

func TestFetchZhipuCodingPlanResetStatus(t *testing.T) {
	var gotAuth, gotBigmodel string
	withZhipuResetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/coding-plan/reset/status", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		gotBigmodel = r.Header.Get("X-Bigmodel-Authorization")
		assert.Equal(t, "PERSONAL", r.Header.Get("Bigmodel-Target-Type"))
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"code":0,"msg":"","data":{"available_five_hour_resets":[{"expire_at":1786600000000},{"expire_at":1786700000000}],"available_week_resets":[],"latest_five_hour_reset_history":{"used_at":1786500000000},"latest_week_reset_history":null,"has_unread_history":true}}`)
		assert.NoError(t, err)
	})

	status, err := FetchZhipuCodingPlanResetStatus(context.Background(), http.DefaultClient, "zcode-jwt", "console-token")
	require.NoError(t, err)
	assert.Equal(t, "Bearer zcode-jwt", gotAuth)
	assert.Equal(t, "console-token", gotBigmodel)
	require.Len(t, status.AvailableFiveHourResets, 2)
	assert.Equal(t, int64(1786600000000), status.AvailableFiveHourResets[0].ExpireAt)
	assert.Empty(t, status.AvailableWeekResets)
	require.NotNil(t, status.LatestFiveHourReset)
	assert.Equal(t, int64(1786500000000), status.LatestFiveHourReset.UsedAt)
	assert.Nil(t, status.LatestWeekReset)
	assert.True(t, status.HasUnreadHistory)
}

func TestFetchZhipuCodingPlanResetStatusBusinessError(t *testing.T) {
	withZhipuResetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"code":3302,"msg":"no card available"}`)
		assert.NoError(t, err)
	})
	_, err := FetchZhipuCodingPlanResetStatus(context.Background(), http.DefaultClient, "zcode-jwt", "console-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no card available")
}

func TestFetchZhipuCodingPlanResetStatusRequiresZcodeJWT(t *testing.T) {
	_, err := FetchZhipuCodingPlanResetStatus(context.Background(), http.DefaultClient, "", "console-token")
	require.ErrorIs(t, err, ErrZhipuResetCredentialUnsupported)
}

func TestUseZhipuCodingPlanReset(t *testing.T) {
	var body atomic.Value
	withZhipuResetTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/coding-plan/reset/use", r.URL.Path)
		raw, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		body.Store(string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"code":0,"msg":"","data":{"used":true}}`)
		assert.NoError(t, err)
	})

	require.NoError(t, UseZhipuCodingPlanReset(context.Background(), http.DefaultClient, "zcode-jwt", "console-token", ZhipuCodingPlanResetTypeWeek))
	payload, ok := body.Load().(string)
	require.True(t, ok)
	assert.Contains(t, payload, `"reset_type":"WEEK"`)
	assert.Contains(t, payload, "idempotency_key")

	require.Error(t, UseZhipuCodingPlanReset(context.Background(), http.DefaultClient, "zcode-jwt", "console-token", "DAILY"))
}
