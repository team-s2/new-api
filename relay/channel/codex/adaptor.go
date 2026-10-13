package codex

import (
	"encoding/json"
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
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("codex channel: endpoint not supported")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/messages endpoint not supported")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("codex channel: endpoint not supported")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/chat/completions endpoint not supported")
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/rerank endpoint not supported")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("codex channel: /v1/embeddings endpoint not supported")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	isCompact := info != nil && info.RelayMode == relayconstant.RelayModeResponsesCompact
	if info != nil && info.IsChannelTest && isCodexResponsesLiteModel(info.UpstreamModelName) {
		request.ParallelToolCalls = json.RawMessage("false")
		if request.Reasoning == nil {
			request.Reasoning = &dto.Reasoning{}
		} else {
			reasoning := *request.Reasoning
			request.Reasoning = &reasoning
		}
		request.Reasoning.Context = json.RawMessage(`"all_turns"`)
	}

	if info != nil && info.ChannelSetting.SystemPrompt != "" {
		systemPrompt := info.ChannelSetting.SystemPrompt

		if len(request.Instructions) == 0 {
			if b, err := common.Marshal(systemPrompt); err == nil {
				request.Instructions = b
			} else {
				return nil, err
			}
		} else if info.ChannelSetting.SystemPromptOverride {
			var existing string
			if err := common.Unmarshal(request.Instructions, &existing); err == nil {
				existing = strings.TrimSpace(existing)
				if existing == "" {
					if b, err := common.Marshal(systemPrompt); err == nil {
						request.Instructions = b
					} else {
						return nil, err
					}
				} else {
					if b, err := common.Marshal(systemPrompt + "\n" + existing); err == nil {
						request.Instructions = b
					} else {
						return nil, err
					}
				}
			} else {
				if b, err := common.Marshal(systemPrompt); err == nil {
					request.Instructions = b
				} else {
					return nil, err
				}
			}
		}
	}
	// Codex backend requires the `instructions` field to be present.
	// Keep it consistent with Codex CLI behavior by defaulting to an empty string.
	if len(request.Instructions) == 0 {
		request.Instructions = json.RawMessage(`""`)
	}

	if isCompact {
		return request, nil
	}
	// codex: store must be false
	request.Store = json.RawMessage("false")
	// rm max_output_tokens
	request.MaxOutputTokens = nil
	request.Temperature = nil
	request.FrequencyPenalty = nil
	request.PresencePenalty = nil

	// The native CLI always advertises the fields below; supply them so the
	// upstream treats the request as a genuine codex-tui call.
	request.Include = canonicalCodexInclude(request.Include, request.Tools)
	if !isCodexResponsesLiteRequest(c, &request) {
		request.ParallelToolCalls = json.RawMessage("true")
		if !strings.HasSuffix(strings.TrimSpace(info.UpstreamModelName), "spark") {
			request.Tools = ensureCodexImageGenerationTool(request.Tools)
		}
	}
	request.Input = convertSystemRoleToDeveloper(request.Input)

	// Session continuity: keep the client-supplied prompt_cache_key when
	// present and derive a stable one from the API token otherwise. The
	// affinity rule keys channel stickiness on prompt_cache_key, so the
	// supplemented value also pins the request to one channel.
	if sessionID := resolveCodexSessionID(c, info); sessionID != "" {
		if key, err := common.Marshal(sessionID); err == nil {
			request.PromptCacheKey = key
		}
	}
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case relayconstant.RelayModeAlphaSearch:
		// Alpha search responses are handled by relay.AlphaSearchHelper.
		return nil, types.NewError(errors.New("codex channel: alpha search response should be handled by AlphaSearchHelper"), types.ErrorCodeInvalidRequest)
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		if info.IsStream {
			return openai.OpenaiImageStreamHandler(c, info, resp)
		}
		return openai.OpenaiImageHandler(c, info, resp)
	case relayconstant.RelayModeResponsesCompact:
		return openai.OaiResponsesCompactionHandler(c, resp)
	case relayconstant.RelayModeResponses:
		if info.IsStream {
			return openai.OaiResponsesStreamHandler(c, info, resp)
		}
		return openai.OaiResponsesHandler(c, info, resp)
	default:
		return nil, types.NewError(errors.New("codex channel: endpoint not supported"), types.ErrorCodeInvalidRequest)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	var path string
	switch info.RelayMode {
	case relayconstant.RelayModeResponses:
		path = "/backend-api/codex/responses"
	case relayconstant.RelayModeResponsesCompact:
		path = "/backend-api/codex/responses/compact"
	case relayconstant.RelayModeAlphaSearch:
		path = "/backend-api/codex/alpha/search"
	case relayconstant.RelayModeImagesGenerations:
		if strings.TrimSpace(info.UpstreamModelName) != codexImageModel {
			return "", newCodexImageRequestError("codex channel: image endpoints only support %s, got %q", codexImageModel, info.UpstreamModelName)
		}
		path = "/backend-api/codex/images/generations"
	case relayconstant.RelayModeImagesEdits:
		if strings.TrimSpace(info.UpstreamModelName) != codexImageModel {
			return "", newCodexImageRequestError("codex channel: image endpoints only support %s, got %q", codexImageModel, info.UpstreamModelName)
		}
		path = "/backend-api/codex/images/edits"
	default:
		return "", errors.New("codex channel: endpoint not supported")
	}
	return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, path, info.ChannelType), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)

	key := strings.TrimSpace(info.ApiKey)
	if !strings.HasPrefix(key, "{") {
		return errors.New("codex channel: key must be a JSON object")
	}

	oauthKey, err := ParseOAuthKey(key)
	if err != nil {
		return err
	}

	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	accountID := strings.TrimSpace(oauthKey.AccountID)

	if accessToken == "" {
		return errors.New("codex channel: access_token is required")
	}
	if accountID == "" {
		return errors.New("codex channel: account_id is required")
	}

	req.Set("Authorization", "Bearer "+accessToken)
	req.Set("chatgpt-account-id", accountID)

	isImageRequest := info.RelayMode == relayconstant.RelayModeImagesGenerations || info.RelayMode == relayconstant.RelayModeImagesEdits

	// Native codex-tui headers the upstream expects on every path: pass the
	// client's values through (a real codex CLI client sends them), otherwise
	// supply the codex-tui defaults.
	passthroughCodexNativeHeaders(c, req)
	if req.Get("User-Agent") == "" {
		req.Set("User-Agent", CodexUserAgent())
	}
	if req.Get("originator") == "" {
		req.Set("originator", codexOriginator)
	}
	if sessionID := resolveCodexSessionID(c, info); sessionID != "" {
		if req.Get("Session_id") == "" && req.Get("Session-Id") == "" {
			req.Set("session_id", sessionID)
		}
	}

	if isImageRequest {
		req.Del("OpenAI-Beta")
	} else {
		if req.Get("OpenAI-Beta") == "" {
			req.Set("OpenAI-Beta", "responses=experimental")
		}
		if info.IsChannelTest && isCodexResponsesLiteModel(info.UpstreamModelName) {
			req.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
			req.Set("Version", "0.144.1")
		}
	}

	// chatgpt.com/backend-api/codex/responses is strict about Content-Type.
	// Clients may omit it or include parameters like `application/json; charset=utf-8`,
	// which can be rejected by the upstream. Force the exact media type.
	req.Set("Content-Type", "application/json")
	if info.IsStream {
		req.Set("Accept", "text/event-stream")
	} else if isImageRequest || req.Get("Accept") == "" {
		req.Set("Accept", "application/json")
	}

	return nil
}
