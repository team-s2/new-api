package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayDoesNotAppendJSONErrorAfterResponseStarted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":`))
	c.Request.Header.Set("Content-Type", "application/json")

	const streamedPayload = "data: upstream stream error\n\n"
	c.Status(http.StatusOK)
	_, err := c.Writer.WriteString(streamedPayload)
	require.NoError(t, err)

	Relay(c, types.RelayFormatOpenAIImage)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, streamedPayload, recorder.Body.String())
}
