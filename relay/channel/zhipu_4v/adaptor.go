package zhipu_4v

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	channelconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	openaiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return a.ConvertOpenAIRequest(c, info, openaiRequest)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.ClaudeRequest) (any, error) {
	claudeAdaptor := claude.Adaptor{}
	return claudeAdaptor.ConvertClaudeRequest(c, info, req)
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	info.ConvOptions().WebSearch = encodeWebSearch(info)
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := info.ChannelBaseUrl
	if baseURL == "" {
		baseURL = channelconstant.GetChannelBaseURL(channelconstant.ChannelTypeZhipu_v4)
	}
	specialPlan, hasSpecialPlan := channelconstant.ChannelSpecialBases[baseURL]

	if channelconstant.IsZcodeTrialPlanBase(baseURL) {
		// The trial plan serves only the Anthropic Messages API; every other
		// relay mode has no endpoint here.
		switch info.RelayMode {
		case relayconstant.RelayModeUnknown, relayconstant.RelayModeChatCompletions,
			relayconstant.RelayModeCompletions, relayconstant.RelayModeResponses:
			return fmt.Sprintf("%s/v1/messages", zcodeTrialPlanBaseURL(baseURL)), nil
		}
		return "", errors.New("zcode trial plan: only the Anthropic Messages API is available")
	}

	switch info.RelayFormat {
	case types.RelayFormatClaude:
		if hasSpecialPlan && specialPlan.ClaudeBaseURL != "" {
			return fmt.Sprintf("%s/v1/messages", specialPlan.ClaudeBaseURL), nil
		}
		return fmt.Sprintf("%s/api/anthropic/v1/messages", baseURL), nil
	default:
		switch info.RelayMode {
		case relayconstant.RelayModeEmbeddings:
			if hasSpecialPlan && specialPlan.OpenAIBaseURL != "" {
				return fmt.Sprintf("%s/embeddings", specialPlan.OpenAIBaseURL), nil
			}
			return fmt.Sprintf("%s/api/paas/v4/embeddings", baseURL), nil
		case relayconstant.RelayModeImagesGenerations:
			if hasSpecialPlan && specialPlan.OpenAIBaseURL != "" {
				return fmt.Sprintf("%s/images/generations", specialPlan.OpenAIBaseURL), nil
			}
			return fmt.Sprintf("%s/api/paas/v4/images/generations", baseURL), nil
		case relayconstant.RelayModeResponses:
			if hasSpecialPlan && specialPlan.ResponsesBaseURL != "" {
				return fmt.Sprintf("%s/responses", specialPlan.ResponsesBaseURL), nil
			}
			return fmt.Sprintf("%s/api/v1/responses", baseURL), nil
		default:
			if hasSpecialPlan && specialPlan.OpenAIBaseURL != "" {
				return fmt.Sprintf("%s/chat/completions", specialPlan.OpenAIBaseURL), nil
			}
			return fmt.Sprintf("%s/api/paas/v4/chat/completions", baseURL), nil
		}
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	apiKey := info.ApiKey
	if channelconstant.IsZcodeTrialPlanBase(info.ChannelBaseUrl) {
		token, err := service.ZcodeTrialPlanToken(info.ApiKey)
		if err != nil {
			return err
		}
		req.Set("Authorization", "Bearer "+token)
		// The gateway fingerprints the official client: the User-Agent casing
		// and its version are both significant.
		req.Set("User-Agent", channelconstant.ZcodeTrialPlanUserAgent)
		anthropicVersion := c.Request.Header.Get("anthropic-version")
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		req.Set("anthropic-version", anthropicVersion)
		return nil
	}
	if _, isCodingPlan := channelconstant.ChannelSpecialBases[info.ChannelBaseUrl]; isCodingPlan {
		credential, err := ParseCodingPlanCredential(info.ApiKey)
		if err != nil {
			return err
		}
		apiKey = credential.APIKey
	}
	req.Set("Authorization", "Bearer "+apiKey)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if channelconstant.IsZcodeTrialPlanBase(info.ChannelBaseUrl) {
		claudeAdaptor := claude.Adaptor{}
		return claudeAdaptor.ConvertOpenAIRequest(c, info, request)
	}
	if lo.FromPtrOr(request.TopP, 0) >= 1 {
		request.TopP = lo.ToPtr(0.99)
	}
	return requestOpenAI2Zhipu(*request), nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if channelconstant.IsZcodeTrialPlanBase(info.ChannelBaseUrl) {
		claudeAdaptor := claude.Adaptor{}
		return claudeAdaptor.ConvertOpenAIResponsesRequest(c, info, request)
	}
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	if !channelconstant.IsZcodeTrialPlanBase(info.ChannelBaseUrl) {
		return channel.DoApiRequest(a, c, info, requestBody)
	}

	payload, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, err
	}
	injected, err := injectZcodeTrialPlanSystem(payload)
	if err != nil {
		return nil, err
	}

	if !service.ZcodeTrialPlanAcquire(info.ChannelId) {
		return nil, types.NewErrorWithStatusCode(
			errors.New("zcode trial plan: the plan concurrency limit is reached"),
			types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	}
	// The slot stays taken until the response body is closed, so the defer
	// releases it only when the request never produced a body to read.
	released := false
	defer func() {
		if !released {
			service.ZcodeTrialPlanRelease(info.ChannelId)
		}
	}()

	resp, err := channel.DoApiRequest(a, c, info, bytes.NewReader(injected))
	if err != nil {
		return nil, err
	}
	resp, err = checkZcodeTrialPlanResponse(resp)
	if err != nil {
		return nil, err
	}
	released = true
	resp.Body = &zcodeTrialPlanBody{ReadCloser: resp.Body, channelId: info.ChannelId}
	return resp, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if channelconstant.IsZcodeTrialPlanBase(info.ChannelBaseUrl) {
		// The upstream speaks Anthropic Messages; the Claude response handler
		// converts to the client's own format.
		adaptor := claude.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	}
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		adaptor := claude.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	default:
		if info.RelayMode == relayconstant.RelayModeImagesGenerations {
			return zhipu4vImageHandler(c, resp, info)
		}
		adaptor := openai.Adaptor{}
		return adaptor.DoResponse(c, resp, info)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
