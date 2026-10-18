package antigravity

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptorRequestURLAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		IsStream: false,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeAntigravitySub,
			ChannelBaseUrl: "https://daily-cloudcode-pa.googleapis.com/",
			ApiKey:         `{"access_token":"ya29.test-token","refresh_token":"1//refresh","project_id":"ag-proj-123"}`,
		},
	}

	reqURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent", reqURL)

	info.IsStream = true
	streamURL, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse", streamURL)

	headers := make(http.Header)
	require.NoError(t, adaptor.SetupRequestHeader(c, &headers, info))
	assert.Equal(t, "Bearer ya29.test-token", headers.Get("Authorization"))
	assert.Equal(t, "text/event-stream", headers.Get("Accept"))
	assert.Contains(t, headers.Get("User-Agent"), "antigravity/")
}

func TestWrapAntigravityRequestAndUnwrapResponses(t *testing.T) {
	info := &relaycommon.RelayInfo{
		UserId:          7,
		OriginModelName: "gemini-3-pro-preview",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         42,
			ChannelType:       constant.ChannelTypeAntigravitySub,
			UpstreamModelName: "gemini-3-pro-preview",
			ApiKey:            `{"access_token":"ya29.token","project_id":"my-ag-project"}`,
		},
	}

	geminiReq := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{
				Role: "user",
				Parts: []dto.GeminiPart{
					{Text: "Hello Antigravity"},
				},
			},
		},
		SafetySettings: []dto.GeminiChatSafetySettings{
			{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
		},
	}

	wrapped, err := wrapAntigravityRequest(info, geminiReq)
	require.NoError(t, err)

	rawWrapped, err := common.Marshal(wrapped)
	require.NoError(t, err)

	var envelope map[string]any
	require.NoError(t, common.Unmarshal(rawWrapped, &envelope))
	assert.Equal(t, "my-ag-project", envelope["project"])
	assert.Equal(t, "agent", envelope["requestType"])
	assert.Equal(t, "antigravity", envelope["userAgent"])
	assert.Equal(t, "gemini-3-pro-high", envelope["model"])
	assert.True(t, strings.HasPrefix(envelope["requestId"].(string), "agent-"))

	innerReq, ok := envelope["request"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, innerReq["safetySettings"])
	assert.Nil(t, innerReq["model"])
	assert.NotEmpty(t, innerReq["sessionId"])

	// Claude models set toolConfig.functionCallingConfig.mode = VALIDATED.
	info.UpstreamModelName = "claude-sonnet-4-5"
	claudeWrapped, err := wrapAntigravityRequest(info, geminiReq)
	require.NoError(t, err)
	rawClaude, err := common.Marshal(claudeWrapped)
	require.NoError(t, err)
	var claudeEnv map[string]any
	require.NoError(t, common.Unmarshal(rawClaude, &claudeEnv))
	claudeInner := claudeEnv["request"].(map[string]any)
	toolCfg := claudeInner["toolConfig"].(map[string]any)
	fnCfg := toolCfg["functionCallingConfig"].(map[string]any)
	assert.Equal(t, "VALIDATED", fnCfg["mode"])

	// Non-streaming JSON unwrap.
	wrappedResp := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi!"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}},"traceId":"t-1"}`)
	unwrappedResp := unwrapAntigravityJSONPayload(wrappedResp)
	var geminiResp dto.GeminiChatResponse
	require.NoError(t, common.Unmarshal(unwrappedResp, &geminiResp))
	require.Len(t, geminiResp.Candidates, 1)
	require.Len(t, geminiResp.Candidates[0].Content.Parts, 1)
	assert.Equal(t, "Hi!", geminiResp.Candidates[0].Content.Parts[0].Text)
	assert.Equal(t, 7, geminiResp.UsageMetadata.TotalTokenCount)

	// Streaming SSE unwrap.
	sseStream := strings.Join([]string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello "}]}}]},"traceId":"t-1"}`,
		``,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"world"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"totalTokenCount":7}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	reader := newAntigravitySSEUnwrapReader(io.NopCloser(strings.NewReader(sseStream)))
	defer reader.Close()
	transformed, err := io.ReadAll(reader)
	require.NoError(t, err)
	transformedStr := string(transformed)
	assert.NotContains(t, transformedStr, `"traceId"`)
	assert.Contains(t, transformedStr, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello "}]}}]}`)
	assert.Contains(t, transformedStr, `data: [DONE]`)
}
