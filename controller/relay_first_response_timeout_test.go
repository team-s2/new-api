package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryUpstreamFirstResponseTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	timeoutErr := types.NewErrorWithStatusCode(
		errors.New("upstream first response timed out"),
		types.ErrorCodeUpstreamFirstResponseTimeout,
		http.StatusGatewayTimeout,
	)

	require.True(t, shouldRetry(ctx, timeoutErr, 1), "local first-response timeouts must bypass ordinary 504 retry rules")
	require.False(t, shouldRetry(ctx, timeoutErr, 0), "retry budget still applies")

	ctx.Set("specific_channel_id", 7)
	require.False(t, shouldRetry(ctx, timeoutErr, 1), "specific-channel requests must not switch channels")
}
