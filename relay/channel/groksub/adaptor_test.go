package groksub

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"io"
	"net/http"
	"net/http/httptest"
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
	encoded, err := common.Marshal(key)
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
	require.NoError(t, common.Unmarshal(out.Tools, &tools))
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
	body := newGrokResponseBody(io.NopCloser(strings.NewReader(upstream)), true)
	out, err := io.ReadAll(body)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "ping")
	assert.Contains(t, string(out), "response.output_text.delta")
	assert.Contains(t, string(out), "hello")
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

func TestPingFilterPreservesLongLines(t *testing.T) {
	input := "data: " + strings.Repeat("x", 128<<10) + "\n\n"
	body := newGrokResponseBody(io.NopCloser(strings.NewReader(input)), true)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Len(t, got, len(input))
	assert.Equal(t, input, string(got))
}

type grokTestBody struct {
	io.Reader
	closed bool
}

func (b *grokTestBody) Close() error { b.closed = true; return nil }

func TestPingFilterClosesUpstream(t *testing.T) {
	upstream := &grokTestBody{Reader: strings.NewReader("")}
	require.NoError(t, newGrokResponseBody(upstream, true).Close())
	assert.True(t, upstream.closed)
}

func TestGrokResponseUsage(t *testing.T) {
	service.InitHttpClient()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAIResponses, types.RelayFormatOpenAI, types.RelayFormatClaude} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				text := strings.Repeat("x", 1024)
				payload := `{"id":"resp_test","object":"response","status":"completed","model":"grok-4.6","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":100,"output_tokens":50,"total_tokens":190,"output_tokens_details":{"reasoning_tokens":40}}}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\""+text+"\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+payload+"}\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, payload)
				}))
				defer server.Close()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
				info := testRelayInfo(oauthKeyJSON(t, OAuthKey{AccessToken: "at"}))
				info.ChannelBaseUrl = server.URL
				info.RelayMode = relayconstant.RelayModeResponses
				info.RelayFormat = format
				info.IsStream = stream
				info.UpstreamModelName = "grok-4.6"
				adaptor := &Adaptor{}
				response, err := adaptor.DoRequest(c, info, strings.NewReader(`{}`))
				require.NoError(t, err)
				var got *dto.Usage
				var apiErr *types.NewAPIError
				if format == types.RelayFormatOpenAIResponses {
					var usage any
					usage, apiErr = adaptor.DoResponse(c, response.(*http.Response), info)
					require.Nil(t, apiErr)
					got = usage.(*dto.Usage)
				} else if stream {
					got, apiErr = openai.OaiResponsesToChatStreamHandler(c, info, response.(*http.Response))
				} else {
					got, apiErr = openai.OaiResponsesToChatHandler(c, info, response.(*http.Response))
				}
				require.Nil(t, apiErr)
				require.NotNil(t, got)
				assert.Equal(t, 90, got.CompletionTokens)
				assert.Equal(t, 190, got.TotalTokens)
				if got.BillingUsage != nil {
					require.NotNil(t, got.BillingUsage.OpenAIUsage)
					assert.Equal(t, 90, got.BillingUsage.OpenAIUsage.OutputTokens)
				}
				assert.Contains(t, recorder.Body.String(), text)
				assert.NotContains(t, recorder.Body.String(), `"type":"ping"`)
			})
		}
	}
}

func TestNormalizeGrokUsage(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"already folded", `{"input_tokens":100,"output_tokens":50,"total_tokens":150,"output_tokens_details":{"reasoning_tokens":40}}`, `{"input_tokens":100,"output_tokens":50,"total_tokens":150,"output_tokens_details":{"reasoning_tokens":40}}`},
		{"bounded remainder", `{"input_tokens":100,"output_tokens":50,"total_tokens":170,"output_tokens_details":{"reasoning_tokens":40}}`, `{"input_tokens":100,"output_tokens":70,"total_tokens":170,"output_tokens_details":{"reasoning_tokens":40}}`},
		{"missing details", `{"input_tokens":100,"output_tokens":50,"total_tokens":190}`, `{"input_tokens":100,"output_tokens":50,"total_tokens":190}`},
		{"integer boundary", `{"input_tokens":9223372036854775707,"output_tokens":50,"total_tokens":9223372036854775807,"output_tokens_details":{"reasoning_tokens":9223372036854775807}}`, `{"input_tokens":9223372036854775707,"output_tokens":100,"total_tokens":9223372036854775807,"output_tokens_details":{"reasoning_tokens":9223372036854775807}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeGrokUsage([]byte(`{"usage":`+tc.raw+`}`), "usage")
			require.NoError(t, err)
			assert.JSONEq(t, `{"usage":`+tc.want+`}`, string(got))
		})
	}
}

func TestGrokFlatFunctionSchemas(t *testing.T) {
	req := dto.OpenAIResponsesRequest{Tools: json.RawMessage(`[{"type":"function","name":"no_args"},{"type":"function","name":"with_args","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]`), ToolChoice: json.RawMessage(`{"type":"function","name":"no_args"}`)}
	result, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, req)
	require.NoError(t, err)
	got := result.(dto.OpenAIResponsesRequest)
	assert.JSONEq(t, `[{"type":"function","name":"no_args","parameters":{"type":"object","properties":{}}},{"type":"function","name":"with_args","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]`, string(got.Tools))
	assert.JSONEq(t, string(req.ToolChoice), string(got.ToolChoice))
}
