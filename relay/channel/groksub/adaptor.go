package groksub

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Adaptor relays to the Grok CLI gateway over the OpenAI Responses protocol.
// Only /v1/responses traffic is served natively; chat/completions and Claude
// messages requests are bridged by the host (see ShouldChatCompletionsUseResponsesGlobal).
type Adaptor struct{}

const (
	// Context keys carrying per-request lowering state from Convert to
	// SetupRequestHeader / DoRequest.
	clientToolMappingContextKey = "groksub_client_tool_mapping"
	cacheIdentityContextKey     = "groksub_cache_identity"

	grokConversationIDHeader = "X-Grok-Conv-Id"
)

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	switch info.RelayMode {
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		// Compact is synthesized into a plain Responses turn upstream.
		return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, "/responses", info.ChannelType), nil
	default:
		return "", errors.New("grok subscription channel: endpoint not supported")
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	if err := ApplyOAuthHeaders(*req, info.ApiKey, info.ChannelBaseUrl); err != nil {
		return err
	}
	if identity := c.GetString(cacheIdentityContextKey); identity != "" {
		req.Set(grokConversationIDHeader, identity)
	}
	return nil
}

// ApplyOAuthHeaders authenticates relay and model-list requests with the
// pinned CLI identity; the credential JSON itself is never sent upstream.
func ApplyOAuthHeaders(header http.Header, credential, baseURL string) error {
	oauthKey, err := service.ParseGrokOAuthKey(credential)
	if err != nil {
		return err
	}
	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	if accessToken == "" {
		return errors.New("grok subscription channel: access_token is required")
	}
	header.Set("Authorization", "Bearer "+accessToken)
	service.ApplyGrokCLIHeaders(header, baseURL)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json, text/event-stream")
	return nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if request.Model == "" && info != nil {
		request.Model = info.UpstreamModelName
	}
	model := ResolveModelID(request.Model)
	request.Model = model
	if info != nil {
		info.UpstreamModelName = model
	}

	encoded, err := common.Marshal(request)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := common.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	claudeSession := claudeCodeSessionID(c, payload)
	mapping, err := patchRequestBody(payload, model)
	if err != nil {
		return nil, kitreasoning.AsClientError(err)
	}
	c.Set(clientToolMappingContextKey, mapping)

	if info != nil && info.RelayMode == relayconstant.RelayModeResponsesCompact {
		// /responses/compact is not a conversation turn: no cache identity.
		delete(payload, "prompt_cache_key")
		if err := buildCompactRequestBody(payload); err != nil {
			return nil, kitreasoning.AsClientError(err)
		}
		return payload, nil
	}

	tokenID := 0
	if info != nil {
		tokenID = info.TokenId
	}
	identity := grokCacheIdentity(c, payload, claudeSession, tokenID, model)
	c.Set(cacheIdentityContextKey, identity)
	if identity == "" {
		delete(payload, "prompt_cache_key")
	} else {
		payload["prompt_cache_key"] = identity
	}
	return payload, nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("grok subscription channel: /v1/chat/completions is bridged by the host, not the adaptor")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("grok subscription channel: /v1/messages is bridged by the host, not the adaptor")
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("grok subscription channel: endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("grok subscription channel: endpoint not supported")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("grok subscription channel: endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("grok subscription channel: endpoint not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("grok subscription channel: endpoint not supported")
}

// DoRequest buffers the converted body so a replay-decode failure (an
// undecryptable encrypted reasoning or compaction blob) can be retried once
// with the opaque replay state removed.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	body, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, err
	}
	resp, err := channel.DoApiRequest(a, c, info, bytes.NewReader(body))
	if err == nil && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if retryBody, ok := replayRetryBody(body, errorBody); ok {
			resp, err = channel.DoApiRequest(a, c, info, bytes.NewReader(retryBody))
		} else {
			resp.Body = io.NopCloser(bytes.NewReader(errorBody))
		}
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	mapping, _ := c.Value(clientToolMappingContextKey).(*clientToolMapping)
	stream := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	resp.Body = newGrokResponseBody(resp.Body, stream, mapping)
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	return resp, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case relayconstant.RelayModeResponsesCompact:
		// The synthesized turn is always non-stream; reshape the completed
		// body into the compaction item the client expects.
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, types.NewError(readErr, types.ErrorCodeReadResponseBodyFailed)
		}
		compactBody, convErr := convertResponseToCompact(body)
		if convErr != nil {
			return nil, types.NewError(convErr, types.ErrorCodeBadResponseBody)
		}
		resp.Body = io.NopCloser(bytes.NewReader(compactBody))
		return openai.OaiResponsesCompactionHandler(c, resp)
	case relayconstant.RelayModeResponses:
		if info.IsStream {
			return openai.OaiResponsesStreamHandler(c, info, resp)
		}
		return openai.OaiResponsesHandler(c, info, resp)
	default:
		return nil, types.NewError(errors.New("grok subscription channel: endpoint not supported"), types.ErrorCodeInvalidRequest)
	}
}

func (a *Adaptor) GetModelList() []string { return ModelList }
func (a *Adaptor) GetChannelName() string { return ChannelName }
