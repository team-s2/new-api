package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newResponsesStreamHandlerTest(t *testing.T, stream string) (*gin.Context, *httptest.ResponseRecorder, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() {
		constant.StreamingTimeout = previousStreamingTimeout
	})
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(stream)),
	}
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeResponses,
		RelayFormat: types.RelayFormatOpenAIResponses,
		IsStream:    true,
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-5.6-sol",
		},
	}
	return c, recorder, resp, info
}

func TestOaiResponsesStreamHandler_RetriesServerOverloadBeforeVisibleOutput(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_overload","status":"in_progress"}}`,
		"",
		`data: {"type":"response.in_progress","response":{"id":"resp_overload","status":"in_progress"}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_overload","status":"failed","error":{"type":"server_error","code":"server_is_overloaded","message":"server is overloaded"}}}`,
		"",
	}, "\n")
	c, recorder, resp, info := newResponsesStreamHandlerTest(t, stream)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.Equal(t, "server_is_overloaded", apiErr.ToOpenAIError().Code)
	require.Empty(t, recorder.Body.String(), "retryable prelude events must not be committed downstream")
}

func TestOaiResponsesStreamHandler_DoesNotRetryServerOverloadAfterVisibleOutput(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"msg_partial","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_partial","status":"failed","error":{"type":"server_error","code":"server_is_overloaded","message":"server is overloaded"}}}`,
		"",
	}, "\n")
	c, recorder, resp, info := newResponsesStreamHandlerTest(t, stream)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	require.Contains(t, recorder.Body.String(), `"type":"response.created"`)
	require.Contains(t, recorder.Body.String(), `"type":"response.output_item.added"`)
	require.Contains(t, recorder.Body.String(), `"type":"response.failed"`)
}

func TestOaiResponsesStreamHandler_FlushesBufferedPreludeOnSuccess(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ok","status":"in_progress"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ok","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`,
		"",
	}, "\n")
	c, recorder, resp, info := newResponsesStreamHandlerTest(t, stream)

	usage, apiErr := OaiResponsesStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.Equal(t, &dto.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, usage)
	require.Contains(t, recorder.Body.String(), `"type":"response.created"`)
	require.Contains(t, recorder.Body.String(), `"type":"response.completed"`)
}

func TestResponsesStreamServerOverloadedError_RecognizesCodexCodes(t *testing.T) {
	for _, testCase := range []struct {
		code string
		want bool
	}{
		{code: "server_is_overloaded", want: true},
		{code: "slow_down", want: true},
		{code: "rate_limit_exceeded", want: false},
	} {
		t.Run(testCase.code, func(t *testing.T) {
			streamResponse := dto.ResponsesStreamResponse{
				Type: "response.failed",
				Response: &dto.OpenAIResponsesResponse{
					Error: types.OpenAIError{
						Message: "upstream failure",
						Code:    testCase.code,
					},
				},
			}

			apiErr := responsesStreamServerOverloadedError(streamResponse)
			if testCase.want {
				require.NotNil(t, apiErr)
				require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
			} else {
				require.Nil(t, apiErr)
			}
		})
	}
}
