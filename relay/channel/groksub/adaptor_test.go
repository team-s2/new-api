package groksub

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveModelID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", DefaultTextModel},
		{"grok", DefaultTextModel},
		{"grok-latest", DefaultTextModel},
		{"xai/grok-4.7", "grok-4.7"},
		{"grok/grok-4.7-latest", "grok-4.7"},
		{"grok-build", "grok-build-0.1"},
		{"grok-composer", "grok-composer-2.5-fast"},
		{"grok-4.20-multi-agent", "grok-4.20-multi-agent-0309"},
		{"grok-custom-model", "grok-custom-model"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ResolveModelID(tc.in), "input %q", tc.in)
	}
}

func testRelayInfo(apiKey string) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	info.ApiKey = apiKey
	info.ChannelBaseUrl = "https://cli-chat-proxy.grok.com/v1"
	return info
}

func oauthKeyJSON(t *testing.T, key OAuthKey) string {
	t.Helper()
	encoded, err := json.Marshal(key)
	require.NoError(t, err)
	return string(encoded)
}

func TestSetupRequestHeaderCLIIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
	c.Request.Header.Set("User-Agent", "curl/8.0")

	info := testRelayInfo(oauthKeyJSON(t, OAuthKey{AccessToken: "at"}))
	header := http.Header{}
	err := (&Adaptor{}).SetupRequestHeader(c, &header, info)
	require.NoError(t, err)

	assert.Equal(t, "Bearer at", header.Get("Authorization"))
	assert.Equal(t, CLIUserAgent(), header.Get("User-Agent"), "client UA must be replaced by the pinned CLI UA")
	assert.Equal(t, cliClientVersion, header.Get("x-grok-client-version"))
	assert.Equal(t, cliClientIdentifier, header.Get("x-grok-client-identifier"))
	assert.Equal(t, cliClientMode, header.Get("X-Grok-Client-Mode"))
	assert.Equal(t, cliTokenAuth, header.Get("X-XAI-Token-Auth"))
	assert.Equal(t, "application/json", header.Get("Content-Type"))
}

func TestSetupRequestHeaderRejectsInvalidKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)

	info := testRelayInfo("not-json")
	header := http.Header{}
	err := (&Adaptor{}).SetupRequestHeader(c, &header, info)
	require.Error(t, err)

	info = testRelayInfo(oauthKeyJSON(t, OAuthKey{}))
	err = (&Adaptor{}).SetupRequestHeader(c, &header, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access_token")
}

func TestConvertOpenAIResponsesRequestSanitization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	info := testRelayInfo(oauthKeyJSON(t, OAuthKey{AccessToken: "at"}))
	info.UpstreamModelName = "grok-4.6"

	request := dto.OpenAIResponsesRequest{
		Model:                "grok",
		Input:                json.RawMessage(`[{"type":"message","role":"user","content":null},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`),
		Metadata:             json.RawMessage(`{}`),
		Store:                json.RawMessage(`true`),
		SafetyIdentifier:     json.RawMessage(`"u"`),
		PromptCacheRetention: json.RawMessage(`"24h"`),
		Include:              json.RawMessage(`["reasoning.encrypted_content"]`),
		Tools:                json.RawMessage(`[{"type":"function","function":{"name":"ok","parameters":{"type":"object"}}},{"type":"function","function":{"name":"bad","parameters":null}},{"type":"local_shell"}]`),
		ToolChoice:           json.RawMessage(`{"type":"function","name":"missing_fn"}`),
	}
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, request)
	require.NoError(t, err)
	out, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)

	assert.Equal(t, "grok-4.6", out.Model)
	assert.Nil(t, out.Metadata)
	assert.Nil(t, out.SafetyIdentifier)
	assert.Nil(t, out.PromptCacheRetention)
	assert.Nil(t, out.Include)
	assert.Equal(t, json.RawMessage("false"), out.Store)

	// null content item dropped; the kept message survives
	assert.NotContains(t, string(out.Input), "null")

	var tools []map[string]any
	require.NoError(t, json.Unmarshal(out.Tools, &tools))
	require.Len(t, tools, 2, "unsupported local_shell dropped")
	assert.Equal(t, "function", tools[0]["type"])
	fn1 := tools[0]["function"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "object"}, fn1["parameters"])
	fn2 := tools[1]["function"].(map[string]any)
	params := fn2["parameters"].(map[string]any)
	assert.Equal(t, "object", params["type"])
	assert.NotNil(t, params["properties"])

	// dangling tool_choice name not declared as a kept function is dropped
	assert.Nil(t, out.ToolChoice)
}

func TestSanitizeReasoning(t *testing.T) {
	build := func(model, effort string) *dto.OpenAIResponsesRequest {
		return &dto.OpenAIResponsesRequest{Model: model, Reasoning: &dto.Reasoning{Effort: effort}}
	}

	req := build("grok-4.6", "minimal")
	sanitizeReasoning(req, req.Model)
	assert.Equal(t, "low", req.Reasoning.Effort)

	req = build("grok-4.7", "xhigh")
	sanitizeReasoning(req, req.Model)
	assert.Equal(t, "xhigh", req.Reasoning.Effort)

	req = build("grok-4.5", "xhigh")
	sanitizeReasoning(req, req.Model)
	assert.Equal(t, "high", req.Reasoning.Effort)

	req = build("grok-4.5", "max")
	sanitizeReasoning(req, req.Model)
	assert.Equal(t, "high", req.Reasoning.Effort)

	req = build("grok-3-mini", "high")
	sanitizeReasoning(req, req.Model)
	assert.Equal(t, "high", req.Reasoning.Effort)

	req = build("grok-4", "high")
	sanitizeReasoning(req, req.Model)
	assert.Nil(t, req.Reasoning, "unknown family must drop reasoning entirely")

	req = build("grok-composer-2.5-fast", "high")
	sanitizeReasoning(req, req.Model)
	assert.Nil(t, req.Reasoning, "composer models must drop reasoning")
}

func TestPingFilterDropsPingFrames(t *testing.T) {
	upstream := "event: ping\n" +
		"data: {\"type\":\"ping\"}\n" +
		"\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n" +
		"\n"
	body := newGrokPingFilterBody(io.NopCloser(strings.NewReader(upstream)))
	out, err := io.ReadAll(body)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "ping")
	assert.Contains(t, string(out), "response.output_text.delta")
	assert.Contains(t, string(out), "hello")
}

func TestAdaptGrokUsageFoldsReasoningTokens(t *testing.T) {
	// total == input+output: reasoning already folded, nothing to add
	usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	usage.CompletionTokenDetails.ReasoningTokens = 50
	got := adaptGrokUsage(usage).(*dto.Usage)
	assert.Equal(t, 50, got.CompletionTokens)
	assert.Equal(t, 150, got.TotalTokens)

	// total > input+output: reasoning reported separately, fold bounded by remainder
	usage = &dto.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 190}
	usage.CompletionTokenDetails.ReasoningTokens = 50
	got = adaptGrokUsage(usage).(*dto.Usage)
	assert.Equal(t, 90, got.CompletionTokens)
	assert.Equal(t, 190, got.TotalTokens)
}

func TestGetRequestURL(t *testing.T) {
	info := testRelayInfo(oauthKeyJSON(t, OAuthKey{AccessToken: "at"}))
	info.RelayMode = relayconstant.RelayModeResponses
	url, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://cli-chat-proxy.grok.com/v1/responses", url)
}

func TestDoResponseUnsupportedMode(t *testing.T) {
	info := testRelayInfo(oauthKeyJSON(t, OAuthKey{AccessToken: "at"}))
	info.RelayMode = relayconstant.RelayModeChatCompletions
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(""))}
	_, apiErr := (&Adaptor{}).DoResponse(nil, resp, info)
	require.NotNil(t, apiErr)
	assert.Contains(t, apiErr.Error(), "endpoint not supported")
}
