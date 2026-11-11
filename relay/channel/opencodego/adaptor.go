package opencodego

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// Adaptor relays OpenCode Go subscription traffic without protocol conversion.
// Each relay format keeps its native request and response shape; only the
// upstream path, bearer credential and session header are channel-specific.
type Adaptor struct {
	openaiAdaptor openai.Adaptor
	claudeAdaptor claude.Adaptor
	geminiAdaptor gemini.Adaptor
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.openaiAdaptor.Init(info)
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := constant.NormalizeOpenCodeGoBaseURL(info.ChannelBaseUrl)

	switch info.GetFinalRequestRelayFormat() {
	case types.RelayFormatClaude:
		return baseURL + anthropicMessagesPath, nil
	case types.RelayFormatGemini:
		action := "generateContent"
		if info.IsStream {
			action = "streamGenerateContent?alt=sse"
			info.DisablePing = true
		}
		model := url.PathEscape(info.UpstreamModelName)
		return baseURL + googleModelsPath + model + ":" + action, nil
	case types.RelayFormatOpenAIResponses:
		return baseURL + openAIResponsesPath, nil
	default:
		return baseURL + openAIChatCompletionsPath, nil
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	header.Set("Authorization", "Bearer "+strings.TrimSpace(info.ApiKey))
	header.Set("User-Agent", "opencode/2.2.5")
	header.Set("HTTP-Referer", "https://opencode.ai/")
	header.Set("X-Title", "opencode")
	header.Set("x-opencode-session", deriveOpenCodeGoSession(c, info))

	if info.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		anthropicVersion := ""
		if c != nil && c.Request != nil {
			anthropicVersion = c.Request.Header.Get("anthropic-version")
		}
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		header.Set("anthropic-version", anthropicVersion)
		claude.CommonClaudeHeadersOperation(c, header, info)
	}
	return nil
}

// deriveOpenCodeGoSession returns the stable session id the gateway uses for
// routing and prompt-cache reuse. A client-supplied x-opencode-session is
// preserved; otherwise a stable per user/channel/model value is derived. The
// fallback cannot follow individual conversations, but it keeps one user's
// traffic on the same route without inventing an identifier from message text.
func deriveOpenCodeGoSession(c *gin.Context, info *relaycommon.RelayInfo) string {
	if c != nil && c.Request != nil {
		if session := strings.TrimSpace(c.Request.Header.Get("x-opencode-session")); session != "" && len(session) <= 64 && printableASCII(session) {
			return session
		}
	}
	seed := "opencode-go"
	if info != nil {
		seed = fmt.Sprintf("opencode-go:%d:%d:%s", info.ChannelId, info.UserId, info.OriginModelName)
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}

func printableASCII(value string) bool {
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return a.openaiAdaptor.ConvertOpenAIRequest(c, info, request)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return a.openaiAdaptor.ConvertOpenAIResponsesRequest(c, info, request)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return a.claudeAdaptor.ConvertClaudeRequest(c, info, request)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return a.geminiAdaptor.ConvertGeminiRequest(c, info, request)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("opencode go subscription does not support rerank requests")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("opencode go subscription does not support embedding requests")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("opencode go subscription does not support audio requests")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("opencode go subscription does not support image generation requests")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	switch info.GetFinalRequestRelayFormat() {
	case types.RelayFormatClaude:
		return a.claudeAdaptor.DoResponse(c, resp, info)
	case types.RelayFormatGemini:
		return a.geminiAdaptor.DoResponse(c, resp, info)
	default:
		return a.openaiAdaptor.DoResponse(c, resp, info)
	}
}
