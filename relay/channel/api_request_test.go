package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newStreamingRequestContext(t *testing.T, targetURL string, timeoutSeconds int) (*gin.Context, *httptest.ResponseRecorder, *http.Request, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request, err := http.NewRequest(http.MethodPost, targetURL, nil)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{
		RequestId:  "first-response-timeout-test",
		IsStream:   true,
		RetryIndex: 0,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 7,
			ChannelSetting: dto.ChannelSettings{
				FirstResponseTimeoutSeconds: timeoutSeconds,
			},
		},
	}
	return ctx, recorder, request, info
}

func requireFirstResponseTimeout(t *testing.T, err error) {
	t.Helper()
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, types.ErrorCodeUpstreamFirstResponseTimeout, apiErr.GetErrorCode())
	require.Equal(t, http.StatusGatewayTimeout, apiErr.StatusCode)
}

func requireFirstResponseTimeoutPhase(t *testing.T, ctx *gin.Context, expectedPhase string) {
	t.Helper()
	value, exists := ctx.Get("first_response_timeout_attempts")
	require.True(t, exists)
	attempts, ok := value.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, attempts, 1)
	require.Equal(t, expectedPhase, attempts[0]["phase"])
}

func TestDoRequestFirstResponseTimeoutWaitingResponseHeaders(t *testing.T) {
	service.InitHttpClient()
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()

	ctx, recorder, request, info := newStreamingRequestContext(t, server.URL, 1)
	started := time.Now()
	resp, err := doRequest(ctx, request, info)
	require.Nil(t, resp)
	requireFirstResponseTimeout(t, err)
	requireFirstResponseTimeoutPhase(t, ctx, "waiting_response_headers")
	require.Less(t, time.Since(started), 2*time.Second)
	require.Empty(t, recorder.Header().Get("Content-Type"), "the downstream stream must not be committed before the gate")
	require.Eventually(t, func() bool {
		select {
		case <-cancelled:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

func TestDoRequestFirstResponseTimeoutIgnoresSSECommentsAndPings(t *testing.T) {
	service.InitHttpClient()
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\nevent: ping\ndata: {}\n\ndata: ping\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()

	ctx, recorder, request, info := newStreamingRequestContext(t, server.URL, 1)
	resp, err := doRequest(ctx, request, info)
	require.Nil(t, resp)
	requireFirstResponseTimeout(t, err)
	requireFirstResponseTimeoutPhase(t, ctx, "waiting_first_stream_event")
	require.Empty(t, recorder.Header().Get("Content-Type"))
	require.Eventually(t, func() bool {
		select {
		case <-cancelled:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

func TestDoRequestFirstResponseTimeoutStartsAfterRequestBodyWritten(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: ok\n\n")
	}))
	defer server.Close()

	ctx, _, _, info := newStreamingRequestContext(t, server.URL, 1)
	bodyReader, bodyWriter := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, server.URL, bodyReader)
	require.NoError(t, err)
	writeResult := make(chan error, 1)
	go func() {
		time.Sleep(1100 * time.Millisecond)
		_, writeErr := bodyWriter.Write([]byte("{}"))
		if closeErr := bodyWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		writeResult <- writeErr
	}()

	started := time.Now()
	resp, err := doRequest(ctx, request, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	require.GreaterOrEqual(t, time.Since(started), time.Second)
	require.NoError(t, <-writeResult)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: ok\n\n", string(body))
}

func TestDoRequestFirstResponseTimeoutPreservesPrefetchedSSEData(t *testing.T) {
	service.InitHttpClient()
	const firstChunk = ": keepalive\n\ndata: {\"type\":\"response.created\"}\n\n"
	const finalChunk = "data: [DONE]\n\n"
	const upstreamStream = firstChunk + finalChunk
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, firstChunk)
		w.(http.Flusher).Flush()
		time.Sleep(1100 * time.Millisecond)
		fmt.Fprint(w, finalChunk)
	}))
	defer server.Close()

	ctx, recorder, request, info := newStreamingRequestContext(t, server.URL, 1)
	resp, err := doRequest(ctx, request, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, upstreamStream, string(body))
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestDoRequestFirstResponseTimeoutDisabledKeepsExistingBehavior(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: ok\n\n")
	}))
	defer server.Close()

	ctx, recorder, request, info := newStreamingRequestContext(t, server.URL, 0)
	resp, err := doRequest(ctx, request, info)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestDoRequestClientCancellationDoesNotBecomeFirstResponseTimeout(t *testing.T) {
	service.InitHttpClient()
	requestReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestReceived)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, _, request, info := newStreamingRequestContext(t, server.URL, 5)
	clientRequest, err := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.NoError(t, err)
	clientCtx, cancelClient := context.WithCancel(clientRequest.Context())
	ctx.Request = clientRequest.WithContext(clientCtx)
	type requestResult struct {
		resp *http.Response
		err  error
	}
	result := make(chan requestResult, 1)
	go func() {
		resp, requestErr := doRequest(ctx, request, info)
		result <- requestResult{resp: resp, err: requestErr}
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive request")
	}
	cancelClient()
	var outcome requestResult
	select {
	case outcome = <-result:
	case <-time.After(time.Second):
		t.Fatal("request did not stop after client cancellation")
	}
	require.Nil(t, outcome.resp)
	var apiErr *types.NewAPIError
	require.True(t, errors.As(outcome.err, &apiErr))
	require.NotEqual(t, types.ErrorCodeUpstreamFirstResponseTimeout, apiErr.GetErrorCode())
	require.True(t, types.IsSkipRetryError(apiErr))
}

func TestProcessHeaderOverride_ChannelTestSkipsPassthroughRules(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestProcessHeaderOverride_ChannelTestSkipsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	_, ok := headers["x-upstream-trace"]
	require.False(t, ok)
}

func TestProcessHeaderOverride_NonTestKeepsClientHeaderPlaceholder(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Upstream-Trace": "{client_header:X-Trace-Id}",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-upstream-trace"])
}

func TestProcessHeaderOverride_RuntimeOverrideIsFinalHeaderMap(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		IsChannelTest:             false,
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-static":  "runtime-value",
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
				"X-Legacy": "legacy-only",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "runtime-value", headers["x-static"])
	require.Equal(t, "runtime-only", headers["x-runtime"])
	_, exists := headers["x-legacy"]
	require.False(t, exists)
}

func TestProcessHeaderOverride_PassthroughSkipsAcceptEncoding(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("X-Trace-Id", "trace-123")
	ctx.Request.Header.Set("Accept-Encoding", "gzip")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			HeadersOverride: map[string]any{
				"*": "",
			},
		},
	}

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "trace-123", headers["x-trace-id"])

	_, hasAcceptEncoding := headers["accept-encoding"]
	require.False(t, hasAcceptEncoding)
}

func TestProcessHeaderOverride_PassHeadersTemplateSetsRuntimeHeaders(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("Originator", "Codex CLI")
	ctx.Request.Header.Set("Session_id", "sess-123")

	info := &relaycommon.RelayInfo{
		IsChannelTest: false,
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
			"Session_id": "sess-123",
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator", "Session_id", "X-Codex-Beta-Features"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-value",
			},
		},
	}

	_, err := relaycommon.ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-4.1"}`), info)
	require.NoError(t, err)
	require.True(t, info.UseRuntimeHeadersOverride)
	require.Equal(t, "Codex CLI", info.RuntimeHeadersOverride["originator"])
	require.Equal(t, "sess-123", info.RuntimeHeadersOverride["session_id"])
	_, exists := info.RuntimeHeadersOverride["x-codex-beta-features"]
	require.False(t, exists)
	require.Equal(t, "legacy-value", info.RuntimeHeadersOverride["x-static"])

	headers, err := processHeaderOverride(info, ctx)
	require.NoError(t, err)
	require.Equal(t, "Codex CLI", headers["originator"])
	require.Equal(t, "sess-123", headers["session_id"])
	_, exists = headers["x-codex-beta-features"]
	require.False(t, exists)

	upstreamReq := httptest.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	applyHeaderOverrideToRequest(upstreamReq, headers)
	require.Equal(t, "Codex CLI", upstreamReq.Header.Get("Originator"))
	require.Equal(t, "sess-123", upstreamReq.Header.Get("Session_id"))
	require.Empty(t, upstreamReq.Header.Get("X-Codex-Beta-Features"))
}
