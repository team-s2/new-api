package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesStreamError_WritesFailureAfterStreamWasCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetEventStreamHeaders(c)
	_, err := c.Writer.Write([]byte(": PING\n\n"))
	require.NoError(t, err)

	err = ResponsesStreamError(c, types.OpenAIError{
		Message: "server is overloaded",
		Type:    "server_error",
		Code:    "server_is_overloaded",
	})

	require.NoError(t, err)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Contains(t, recorder.Body.String(), "event: response.failed")
	require.Contains(t, recorder.Body.String(), `"status":"failed"`)
	require.Contains(t, recorder.Body.String(), `"code":"server_is_overloaded"`)
}
