package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGetRequestURLAlphaSearch(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeCodex,
			ChannelBaseUrl: "https://chatgpt.com",
		},
		RelayMode: relayconstant.RelayModeAlphaSearch,
	}

	url, err := adaptor.GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://chatgpt.com/backend-api/codex/alpha/search", url)
}

// The Codex backend rejects these fields, so the adaptor clears them rather
// than forwarding what the client sent.
func TestConvertOpenAIResponsesRequestDropsPenalties(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
	}

	converted, err := adaptor.ConvertOpenAIResponsesRequest(nil, info, dto.OpenAIResponsesRequest{
		Model:            "gpt-5-codex",
		Input:            json.RawMessage(`"hello"`),
		MaxOutputTokens:  lo.ToPtr(uint(128)),
		Temperature:      lo.ToPtr(1.0),
		FrequencyPenalty: json.RawMessage(`1.5`),
		PresencePenalty:  json.RawMessage(`1.5`),
	})
	require.NoError(t, err)

	request, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.Nil(t, request.MaxOutputTokens)
	assert.Nil(t, request.Temperature)
	assert.Nil(t, request.FrequencyPenalty)
	assert.Nil(t, request.PresencePenalty)
}

func newCodexResponsesTestContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	return c
}

// The CLI-shaped body the adaptor must produce: include, parallel_tool_calls,
// the image_generation tool, developer roles, and a prompt_cache_key linked to
// the session identity.
func TestConvertOpenAIResponsesRequestCliShapedBody(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
		TokenKey:    "sk-test-token",
	}
	c := newCodexResponsesTestContext(t, `{"model":"gpt-5.5","input":[{"type":"message","role":"system","content":"x"}]}`)

	converted, err := adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
		Model: "gpt-5.5",
		Input: json.RawMessage(`[{"type":"message","role":"system","content":"x"}]`),
	})
	require.NoError(t, err)

	request, ok := converted.(dto.OpenAIResponsesRequest)
	require.True(t, ok)
	assert.JSONEq(t, `["reasoning.encrypted_content"]`, string(request.Include))
	assert.Equal(t, json.RawMessage("true"), request.ParallelToolCalls)
	assert.JSONEq(t, `false`, string(request.Store))
	assert.Contains(t, string(request.Tools), `"type":"image_generation"`)
	var developerCount int
	for _, item := range gjson.ParseBytes(request.Input).Array() {
		if item.Get("role").String() == "developer" {
			developerCount++
		}
	}
	assert.Equal(t, 1, developerCount)
	assert.NotEmpty(t, request.PromptCacheKey)
	// The supplemented prompt_cache_key must equal the token-derived session id
	// so channel affinity and prompt caching key on the same value.
	assert.Equal(t, CodexSessionUUID("sk-test-token"), string(request.PromptCacheKey)[1:len(request.PromptCacheKey)-1])
}

// prompt_cache_key from the client body wins over the token-derived session.
func TestConvertOpenAIResponsesRequestKeepsClientPromptCacheKey(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
		TokenKey:    "sk-test-token",
	}
	c := newCodexResponsesTestContext(t, `{"model":"gpt-5.5","prompt_cache_key":"client-session"}`)

	converted, err := adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
		Model:          "gpt-5.5",
		PromptCacheKey: json.RawMessage(`"client-session"`),
	})
	require.NoError(t, err)
	request := converted.(dto.OpenAIResponsesRequest)
	assert.Equal(t, json.RawMessage(`"client-session"`), request.PromptCacheKey)
}

// Web search requests include the sources entry; lite requests keep their
// dialect (no parallel_tool_calls forcing, no image tool).
func TestConvertOpenAIResponsesRequestIncludeVariants(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeCodex},
		RelayMode:   relayconstant.RelayModeResponses,
		TokenKey:    "sk-test-token",
	}
	c := newCodexResponsesTestContext(t, `{}`)

	converted, err := adaptor.ConvertOpenAIResponsesRequest(c, info, dto.OpenAIResponsesRequest{
		Model: "gpt-5.5",
		Tools: json.RawMessage(`[{"type":"web_search"}]`),
	})
	require.NoError(t, err)
	request := converted.(dto.OpenAIResponsesRequest)
	assert.JSONEq(t, `["reasoning.encrypted_content","web_search_call.action.sources"]`, string(request.Include))

	liteHeader := newCodexResponsesTestContext(t, `{}`)
	liteHeader.Request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	liteConverted, err := adaptor.ConvertOpenAIResponsesRequest(liteHeader, info, dto.OpenAIResponsesRequest{
		Model: "gpt-5.5",
	})
	require.NoError(t, err)
	liteRequest := liteConverted.(dto.OpenAIResponsesRequest)
	assert.Nil(t, liteRequest.ParallelToolCalls)
	assert.Nil(t, liteRequest.Tools)
}

// SetupRequestHeader stamps the codex-tui identity: UA, originator, session,
// and native header passthrough.
func TestSetupRequestHeaderCodexTuiIdentity(t *testing.T) {
	c := newCodexResponsesTestContext(t, `{"model":"gpt-5.5"}`)
	c.Request.Header.Set("Thread_id", "thread-1")
	c.Request.Header.Set("Version", "0.155.0")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"turn":1}`)
	c.Request.Header.Set("X-Codex-Turn-State", "state-1")
	c.Request.Header.Set("X-Client-Request-Id", "req-1")
	c.Request.Header.Set("X-Codex-Window-Id", "win-1")

	header := http.Header{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: `{"access_token":"oauth-token","account_id":"account-123"}`,
		},
		TokenKey: "sk-test-token",
	}
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))

	assert.Equal(t, codexUserAgent, header.Get("User-Agent"))
	assert.Equal(t, codexOriginator, header.Get("originator"))
	assert.Equal(t, CodexSessionUUID("sk-test-token"), header.Get("session_id"))
	assert.Equal(t, "thread-1", header.Get("Thread_id"))
	assert.Equal(t, "0.155.0", header.Get("Version"))
	assert.Equal(t, `{"turn":1}`, header.Get("X-Codex-Turn-Metadata"))
	assert.Equal(t, "state-1", header.Get("X-Codex-Turn-State"))
	assert.Equal(t, "req-1", header.Get("X-Client-Request-Id"))
	assert.Equal(t, "win-1", header.Get("X-Codex-Window-Id"))
}

// A client session header overrides the derived session id.
func TestSetupRequestHeaderClientSessionWins(t *testing.T) {
	c := newCodexResponsesTestContext(t, `{}`)
	c.Request.Header.Set("Session_id", "client-session")

	header := http.Header{}
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeResponses,
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: `{"access_token":"oauth-token","account_id":"account-123"}`,
		},
		TokenKey: "sk-test-token",
	}
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	assert.Equal(t, "client-session", header.Get("session_id"))
}

// CODEX_USER_AGENT overrides the built-in codex-tui UA.
func TestCodexUserAgentOverride(t *testing.T) {
	original := common.CodexUserAgentOverride
	defer func() { common.CodexUserAgentOverride = original }()

	common.CodexUserAgentOverride = ""
	assert.Equal(t, codexUserAgent, CodexUserAgent())

	common.CodexUserAgentOverride = "custom-agent/1.0"
	assert.Equal(t, "custom-agent/1.0", CodexUserAgent())
}
