package groksub

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
		TokenId:     7,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	info.ApiKey = apiKey
	info.ChannelBaseUrl = "https://cli-chat-proxy.grok.com/v1"
	info.RelayMode = relayconstant.RelayModeResponses
	return info
}

func testContext(headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c, recorder
}

const testOAuthKey = `{"access_token":"at","refresh_token":"rt"}`

func TestSetupRequestHeaderCLIIdentity(t *testing.T) {
	c, _ := testContext(map[string]string{"User-Agent": "curl/8.0"})
	c.Set(cacheIdentityContextKey, "conv-1")
	header := http.Header{}
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, testRelayInfo(testOAuthKey)))

	assert.Equal(t, "Bearer at", header.Get("Authorization"))
	assert.Equal(t, service.GrokCLIUserAgent(), header.Get("User-Agent"), "client UA must be replaced by the pinned CLI UA")
	assert.Equal(t, service.GrokCLIVersion, header.Get("x-grok-client-version"))
	assert.Equal(t, "grok-pager", header.Get("x-grok-client-identifier"))
	assert.Equal(t, "interactive", header.Get("x-grok-client-mode"))
	assert.Equal(t, "xai-grok-cli", header.Get("X-XAI-Token-Auth"))
	assert.Equal(t, "authenticate-response", header.Get("x-authenticateresponse"))
	assert.Equal(t, "conv-1", header.Get(grokConversationIDHeader))
	assert.Equal(t, "application/json", header.Get("Content-Type"))

	for _, key := range []string{"not-json", `{"refresh_token":"rt"}`} {
		require.Error(t, (&Adaptor{}).SetupRequestHeader(c, &http.Header{}, testRelayInfo(key)), key)
	}
}

func convertForTest(t *testing.T, mode int, headers map[string]string, body string) (map[string]any, *gin.Context) {
	t.Helper()
	c, _ := testContext(headers)
	info := testRelayInfo(testOAuthKey)
	info.RelayMode = mode
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(body, &request))
	converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, info, request)
	require.NoError(t, err)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, common.Unmarshal(encoded, &payload))
	return payload, c
}

func TestConvertOpenAIResponsesRequest(t *testing.T) {
	payload, c := convertForTest(t, relayconstant.RelayModeResponses, map[string]string{"session_id": "codex-session"}, `{
		"model": "grok",
		"metadata": {"user_id": "u"},
		"safety_identifier": "u",
		"prompt_cache_retention": "24h",
		"prompt_cache_key": "client-key",
		"store": false,
		"include": ["reasoning.encrypted_content"],
		"reasoning": {"effort": "x-high", "summary": "auto"},
		"tools": [
			{"type": "custom", "name": "apply_patch", "format": {"type": "grammar"}},
			{"type": "tool_search"},
			{"type": "namespace", "name": "mcp__fs", "tools": [{"type": "function", "name": "read", "parameters": {"type": "object"}}]},
			{"type": "function", "name": "union", "strict": true, "parameters": {"anyOf": [{"type": "string"}]}},
			{"type": "function", "name": "untyped", "parameters": {"properties": {"p": {"type": "string"}}}, "defer_loading": true},
			{"type": "local_shell"}
		],
		"tool_choice": {"type": "custom", "name": "apply_patch"},
		"input": [
			{"type": "compaction", "encrypted_content": "CMP", "summary": [{"type": "summary_text", "text": "earlier"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "patch it"}]},
			{"type": "custom_tool_call", "id": "ctc_1", "call_id": "call_1", "name": "apply_patch", "input": "*** Begin Patch"},
			{"type": "custom_tool_call_output", "call_id": "call_1", "output": "done"},
			{"type": "function_call", "call_id": "call_2", "namespace": "mcp__fs", "name": "read", "arguments": "{}"}
		]
	}`)

	assert.Equal(t, DefaultTextModel, payload["model"])
	for _, field := range []string{"metadata", "safety_identifier", "prompt_cache_retention"} {
		assert.NotContains(t, payload, field)
	}
	assert.Equal(t, false, payload["store"], "store passes through unchanged")
	assert.Equal(t, []any{"reasoning.encrypted_content"}, payload["include"], "encrypted reasoning must stay requested for replay")
	assert.Equal(t, map[string]any{"effort": "xhigh", "summary": "auto"}, payload["reasoning"])

	identity := c.GetString(cacheIdentityContextKey)
	assert.Len(t, identity, 36)
	assert.Equal(t, identity, payload["prompt_cache_key"], "client cache key is replaced by the tenant-isolated identity")

	tools := payload["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool := raw.(map[string]any)
		assert.Equal(t, "function", tool["type"])
		assert.NotContains(t, tool, "defer_loading")
		names = append(names, tool["name"].(string))
	}
	assert.Equal(t, []string{"apply_patch", "tool_search", "mcp__fs__read", "union", "untyped"}, names, "client tools are lowered before unsupported types are dropped")
	assert.JSONEq(t, customToolInputSchema, mustJSON(t, tools[0].(map[string]any)["parameters"]))
	assert.Equal(t, false, tools[3].(map[string]any)["strict"])
	assert.Equal(t, map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}, tools[3].(map[string]any)["parameters"])
	assert.Equal(t, map[string]any{"properties": map[string]any{"p": map[string]any{"type": "string"}}}, tools[4].(map[string]any)["parameters"], "object schemas without a union root are untouched")
	assert.Equal(t, map[string]any{"type": "function", "name": "apply_patch"}, payload["tool_choice"])

	input := payload["input"].([]any)
	require.Len(t, input, 6)
	assert.Equal(t, map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "CMP"}, input[0])
	assert.Equal(t, "<conversation_summary>\nearlier\n</conversation_summary>", gjson.Get(mustJSON(t, input[1]), "content.0.text").String())
	call := input[3].(map[string]any)
	assert.Equal(t, "function_call", call["type"])
	assert.Equal(t, "call_1", call["call_id"])
	assert.JSONEq(t, `{"input":"*** Begin Patch"}`, call["arguments"].(string))
	assert.Equal(t, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"}, input[4])
	assert.Equal(t, "mcp__fs__read", input[5].(map[string]any)["name"])
	assert.NotContains(t, input[5], "namespace")

	mapping, _ := c.Value(clientToolMappingContextKey).(*clientToolMapping)
	require.NotNil(t, mapping)
	assert.True(t, mapping.customTools["apply_patch"])
	assert.True(t, mapping.toolSearch)
	assert.Equal(t, namespacedToolName{Namespace: "mcp__fs", Name: "read"}, mapping.namespaceTools["mcp__fs__read"])
}

func TestConvertOpenAIResponsesRequestCacheIdentity(t *testing.T) {
	identity := func(headers map[string]string, body string) string {
		payload, _ := convertForTest(t, relayconstant.RelayModeResponses, headers, body)
		key, _ := payload["prompt_cache_key"].(string)
		return key
	}
	base := `{"model":"grok-4.6","instructions":"be brief","input":"hi"}`
	other := `{"model":"grok-4.6","instructions":"be verbose","input":"hi"}`

	assert.Equal(t, identity(nil, base), identity(nil, base), "a stable prefix maps to one identity")
	assert.NotEqual(t, identity(nil, base), identity(nil, other))
	assert.Equal(t, identity(map[string]string{"session_id": "s"}, base), identity(map[string]string{"session_id": "s"}, other), "explicit sessions outrank the prefix")
	assert.Equal(t,
		identity(map[string]string{"X-Claude-Code-Session-Id": "3f2a"}, base),
		identity(nil, `{"model":"grok-4.6","input":"x","metadata":{"user_id":"user_abc_session_3f2a"}}`),
		"Claude Code sessions resolve from header or metadata")
	assert.Empty(t, identity(nil, `{"model":"grok-4.6"}`), "no seed means no identity rather than a tenant-wide one")
}

func TestConvertOpenAIResponsesRequestCompact(t *testing.T) {
	payload, c := convertForTest(t, relayconstant.RelayModeResponsesCompact, map[string]string{"session_id": "s"}, `{
		"model": "grok-4.6",
		"prompt_cache_key": "k",
		"tools": [{"type": "custom", "name": "apply_patch"}],
		"input": [{"type": "message", "role": "user", "content": "work"}]
	}`)
	input := payload["input"].([]any)
	require.Len(t, input, 2)
	assert.Contains(t, gjson.Get(mustJSON(t, input[1]), "content.0.text").String(), "/tmp/compaction/segment_")
	assert.Equal(t, "none", payload["tool_choice"])
	assert.Equal(t, false, payload["stream"])
	assert.Equal(t, false, payload["store"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, payload["include"])
	assert.NotContains(t, payload, "prompt_cache_key")
	assert.Empty(t, c.GetString(cacheIdentityContextKey))
	assert.Equal(t, "function", payload["tools"].([]any)[0].(map[string]any)["type"], "compact requests are lowered like normal turns")
}

func TestConvertOpenAIResponsesRequestRejectsToolConflicts(t *testing.T) {
	c, _ := testContext(nil)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"tools":[{"type":"custom","name":"x"},{"type":"function","name":"x"}]}`, &request))
	_, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(c, testRelayInfo(testOAuthKey), request)
	require.Error(t, err)
	assert.True(t, kitreasoning.IsClientError(err), "conflicts are client errors (400)")
}

func TestNormalizeReasoning(t *testing.T) {
	cases := []struct {
		model, effort string
		want          any
	}{
		{"grok-4.6", "minimal", map[string]any{"effort": "low", "summary": "auto"}},
		{"grok-4.7", "xhigh", map[string]any{"effort": "xhigh", "summary": "auto"}},
		{"grok-4.5", "x-high", map[string]any{"effort": "high", "summary": "auto"}},
		{"grok-4.5", "max", map[string]any{"effort": "high", "summary": "auto"}},
		{"grok-4.6", "none", map[string]any{"effort": "none", "summary": "auto"}},
		{"grok-4.6", "bogus", map[string]any{"summary": "auto"}},
		{"grok-build-0.1", "high", map[string]any{"summary": "auto"}},
		{"grok-composer-2.5-fast", "high", nil},
	}
	for _, tc := range cases {
		payload := map[string]any{"reasoning": map[string]any{"effort": tc.effort, "summary": "auto"}}
		normalizeReasoning(payload, tc.model)
		if tc.want == nil {
			assert.NotContains(t, payload, "reasoning", tc.model)
			continue
		}
		assert.Equal(t, tc.want, payload["reasoning"], "%s/%s", tc.model, tc.effort)
	}
}

func TestSanitizeModelInput(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{
			name:  "tool output images are lifted into a user turn",
			input: `[{"type":"function_call","call_id":"call_1","name":"look","arguments":"{}"},{"role":"tool","tool_call_id":"call_1","content":[{"type":"output_text","text":"Sunny"},{"type":"image","url":"https://x/img.png"},{"type":"image","url":"data:image/png;base64,"}]}]`,
			want:  `[{"type":"function_call","call_id":"call_1","name":"look","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"Sunny"},{"type":"message","role":"user","content":[{"type":"input_text","text":"[Tool output media for call call_1]"},{"type":"input_image","image_url":"https://x/img.png"}]}]`,
		},
		{
			name:  "outputs may precede their calls",
			input: `[{"role":"tool","tool_call_id":"call_9","content":"42"},{"type":"function_call","call_id":"call_9","name":"calc","arguments":"{}"}]`,
			want:  `[{"type":"function_call_output","call_id":"call_9","output":"42"},{"type":"function_call","call_id":"call_9","name":"calc","arguments":"{}"}]`,
		},
		{
			name:  "single object input and bare strings become messages",
			input: `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}`,
			want:  `[{"type":"message","role":"assistant","content":"ab"}]`,
		},
		{
			name:  "empty parts and empty data URIs are dropped",
			input: `["hello",{"type":"message","role":"user","content":[{"type":"input_text","text":"  "},{"type":"input_image","image_url":"data:image/png;base64,"}]}]`,
			want:  `[{"type":"message","role":"user","content":"hello"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input any
			require.NoError(t, common.UnmarshalJsonStr(tc.input, &input))
			assert.JSONEq(t, tc.want, mustJSON(t, sanitizeModelInput(input)))
		})
	}
	assert.Equal(t, "plain prompt", sanitizeModelInput("plain prompt"))
}

func TestRestoreClientToolPayload(t *testing.T) {
	mapping := &clientToolMapping{
		customTools:    map[string]bool{"apply_patch": true},
		toolSearch:     true,
		namespaceTools: map[string]namespacedToolName{"mcp__fs__read": {Namespace: "mcp__fs", Name: "read"}},
	}
	payload := `{"output":[
		{"type":"function_call","id":"fc_1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"patch\"}"},
		{"type":"function_call","id":"fc_2","call_id":"c2","name":"tool_search","arguments":"{\"query\":\"q\"}"},
		{"type":"function_call","id":"fc_3","call_id":"c3","name":"mcp__fs__read","arguments":"{}"}
	]}`
	restored, changed := restoreClientToolPayload([]byte(payload), mapping)
	require.True(t, changed)
	assert.JSONEq(t, `{"output":[
		{"type":"custom_tool_call","id":"ctc_1","call_id":"c1","name":"apply_patch","input":"patch"},
		{"type":"tool_search_call","id":"tsc_2","call_id":"c2","execution":"client","arguments":{"query":"q"}},
		{"type":"function_call","id":"fc_3","call_id":"c3","name":"read","namespace":"mcp__fs","arguments":"{}"}
	]}`, string(restored))
}

func TestGrokResponseBodyStream(t *testing.T) {
	mapping := &clientToolMapping{customTools: map[string]bool{"apply_patch": true}}
	upstream := strings.Join([]string{
		"event: ping", `data: {"type":"ping","cost":1}`, "",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"apply_patch","arguments":""}}`, "",
		`data: {"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"input\":"}`, "",
		`data: {"type":"response.function_call_arguments.delta","sequence_number":3,"output_index":0,"item_id":"fc_1","delta":"\"patch\"}"}`, "",
		`data: {"type":"response.function_call_arguments.done","sequence_number":4,"output_index":0,"item_id":"fc_1","arguments":"{\"input\":\"patch\"}"}`, "",
		`data: {"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"patch\"}"}}`, "",
		`data: {"type":"response.completed","sequence_number":6,"response":{"output":[{"type":"function_call","id":"fc_1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"patch\"}"}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":4}}}}`, "",
		"data: [DONE]", "",
	}, "\n")
	out, err := io.ReadAll(newGrokResponseBody(io.NopCloser(strings.NewReader(upstream)), true, mapping))
	require.NoError(t, err)

	frames := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	require.Len(t, frames, 7)
	assert.Equal(t, ": ping", frames[0], "ping frames become a comment so the idle timer still sees traffic")
	var events []gjson.Result
	for _, frame := range frames[1:6] {
		data, ok := strings.CutPrefix(frame, "data: ")
		require.True(t, ok, frame)
		events = append(events, gjson.Parse(data))
	}
	assert.Equal(t, "data: [DONE]", frames[6])

	types := make([]string, 0, len(events))
	for index, event := range events {
		types = append(types, event.Get("type").String())
		assert.Equal(t, int64(index+1), event.Get("sequence_number").Int(), "sequence numbers stay continuous")
	}
	assert.Equal(t, []string{
		"response.output_item.added",
		"response.custom_tool_call_input.delta",
		"response.custom_tool_call_input.done",
		"response.output_item.done",
		"response.completed",
	}, types)
	assert.Equal(t, "custom_tool_call", events[0].Get("item.type").String())
	assert.Equal(t, "ctc_1", events[0].Get("item.id").String())
	assert.Equal(t, "patch", events[1].Get("delta").String())
	assert.Equal(t, "patch", events[2].Get("input").String())
	assert.Equal(t, "patch", events[3].Get("item.input").String())
	assert.Equal(t, "custom_tool_call", events[4].Get("response.output.0.type").String())
	assert.Equal(t, int64(5), events[4].Get("response.usage.output_tokens").Int(), "independent reasoning tokens are folded into output")
}

func TestDoRequestRetriesReplayDecodeFailureWithConvertedBody(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","status":"completed","output":[]}`)
	}))
	defer server.Close()

	c, _ := testContext(nil)
	info := testRelayInfo(testOAuthKey)
	info.ChannelBaseUrl = server.URL
	converted := `{"model":"grok-4.6","input":[{"type":"reasoning","encrypted_content":"ENC","summary":[]},{"type":"reasoning","encrypted_content":"ENC2","summary":[{"type":"summary_text","text":"kept"}]},{"type":"message","role":"user","content":"hi"}]}`
	resp, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(converted))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.(*http.Response).StatusCode)

	require.Len(t, bodies, 2)
	assert.JSONEq(t, converted, bodies[0])
	assert.JSONEq(t, `{"model":"grok-4.6","input":[{"type":"reasoning","summary":[]},{"type":"reasoning","summary":[{"type":"summary_text","text":"kept"}]},{"type":"message","role":"user","content":"hi"}]}`, bodies[1],
		"the retry resends the converted upstream body minus the opaque replay state")
}

func TestDoRequestKeepsUnrelatedBadRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":"invalid-argument","error":"model not found"}`)
	}))
	defer server.Close()

	c, _ := testContext(nil)
	info := testRelayInfo(testOAuthKey)
	info.ChannelBaseUrl = server.URL
	resp, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"input":[{"type":"reasoning","encrypted_content":"ENC"}]}`))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.(*http.Response).Body)
	assert.Equal(t, 1, calls)
	assert.Contains(t, string(body), "model not found", "the consumed error body is replayed to the caller")
}

func TestReplayRetryBody(t *testing.T) {
	cases := []struct{ name, request, errorBody, want string }{
		{
			name:      "compaction decode keeps the summary and drops a dangling previous response",
			request:   `{"previous_response_id":"resp_old","input":[{"type":"compaction","encrypted_content":"C","summary":[{"type":"summary_text","text":"s"}]},{"type":"reasoning","summary":[]}]}`,
			errorBody: `{"error":{"message":"failed to deserialize response history"}}`,
			want:      `{"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<conversation_summary>\ns\n</conversation_summary>"}]}]}`,
		},
		{
			name:      "tool outputs keep previous_response_id",
			request:   `{"previous_response_id":"resp_old","input":[{"type":"function_call_output","call_id":"c","output":"ok"},{"type":"reasoning","encrypted_content":"E"}]}`,
			errorBody: `{"error":{"message":"cannot decode encrypted_content"}}`,
			want:      `{"previous_response_id":"resp_old","input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := replayRetryBody([]byte(tc.request), []byte(tc.errorBody))
			require.True(t, ok)
			assert.JSONEq(t, tc.want, string(got))
		})
	}

	for _, errorBody := range []string{`{"code":"invalid-argument","error":"model not found"}`, `{"error":{"message":"cannot decode audio"}}`, ``} {
		_, ok := replayRetryBody([]byte(`{"input":[{"type":"reasoning","encrypted_content":"E"}]}`), []byte(errorBody))
		assert.False(t, ok, errorBody)
	}
	_, ok := replayRetryBody([]byte(`{"input":[{"type":"message","role":"user","content":"hi"}]}`), []byte(`{"code":"invalid_encrypted_content"}`))
	assert.False(t, ok, "nothing to strip means no retry")
}

func TestConvertResponseToCompact(t *testing.T) {
	compact, err := convertResponseToCompact([]byte(`{"id":"resp_1","output":[{"type":"reasoning","encrypted_content":"ENC"},{"type":"message","content":[{"type":"output_text","text":"one"},{"type":"output_text","text":"two"}]}],"output_text":"x"}`))
	require.NoError(t, err)
	item := gjson.GetBytes(compact, "output.0")
	assert.Equal(t, "compaction", item.Get("type").String())
	assert.Equal(t, "ENC", item.Get("encrypted_content").String())
	assert.True(t, strings.HasPrefix(item.Get("id").String(), "cmp_"))
	assert.Equal(t, "one\ntwo", item.Get("summary.0.text").String())
	assert.False(t, gjson.GetBytes(compact, "output_text").Exists())

	_, err = convertResponseToCompact([]byte(`{"output":[{"type":"message","content":[]}]}`))
	require.Error(t, err)
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
				c, recorder := testContext(nil)
				info := testRelayInfo(testOAuthKey)
				info.ChannelBaseUrl = server.URL
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
			got := normalizeGrokUsage([]byte(`{"usage":`+tc.raw+`}`), "usage")
			assert.JSONEq(t, `{"usage":`+tc.want+`}`, string(got))
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
