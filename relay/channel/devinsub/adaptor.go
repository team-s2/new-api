package devinsub

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Adaptor struct {
	openai.Adaptor
}

// devinUpstreamRequest is the internal JSON handoff between Convert* and
// DoRequest (the relay handler marshals whatever Convert* returns).
type devinUpstreamRequest struct {
	Model       string        `json:"model"`
	System      string        `json:"system,omitempty"`
	Prompts     []devinPrompt `json:"prompts"`
	Tools       []devinTool   `json:"tools,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	SessionID   string        `json:"session_id,omitempty"`
	CascadeID   string        `json:"cascade_id,omitempty"`
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.Adaptor.Init(info)
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	base := strings.TrimSpace(info.ChannelBaseUrl)
	if base == "" {
		base = DefaultBaseURL
	}
	return strings.TrimSuffix(base, "/") + ChatRPCPath, nil
}

func (a *Adaptor) SetupRequestHeader(_ *gin.Context, _ *http.Header, _ *relaycommon.RelayInfo) error {
	return nil
}

// dataURLParts splits a data URI into mime type and base64 payload.
func dataURLParts(url string) (mime, b64 string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	head, payload, found := strings.Cut(url[5:], ",")
	if !found {
		return "", "", false
	}
	mime = head
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = mime[:idx]
	}
	if mime == "" {
		mime = "image/png"
	}
	return mime, payload, true
}

// openAIRequestToDevin maps a GeneralOpenAIRequest onto the Devin prompt
// history: system/developer messages merge into the system prompt, user=1,
// assistant=2 (with tool calls + thinking), tool=4 (with tool_call_id).
func openAIRequestToDevin(request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) (*devinUpstreamRequest, error) {
	modelUID, ok := ResolveModelUID(request.Model)
	if !ok {
		return nil, fmt.Errorf("devin subscription channel only serves swe-2 / swe-2-flash family models, got %q", request.Model)
	}

	var systemParts []string
	var prompts []devinPrompt
	for i := range request.Messages {
		msg := &request.Messages[i]
		switch msg.Role {
		case "system", "developer":
			if text := msg.StringContent(); strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			p := devinPrompt{Source: 1}
			if msg.IsStringContent() {
				p.Content = msg.StringContent()
			} else {
				var textParts []string
				for _, part := range msg.ParseContent() {
					switch part.Type {
					case dto.ContentTypeText:
						textParts = append(textParts, part.Text)
					case dto.ContentTypeImageURL:
						if img := part.GetImageMedia(); img != nil {
							if mime, data, ok := dataURLParts(img.Url); ok {
								p.Images = append(p.Images, devinImage{Base64Data: data, MimeType: mime})
							}
						}
					}
				}
				p.Content = strings.Join(textParts, "\n")
			}
			prompts = append(prompts, p)
		case "assistant":
			p := devinPrompt{Source: 2, Content: msg.StringContent(), Thinking: msg.GetReasoningContent()}
			for _, tc := range msg.ParseToolCalls() {
				if tc.Type != "" && tc.Type != "function" && tc.Type != "custom" {
					continue
				}
				p.ToolCalls = append(p.ToolCalls, devinToolCall{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
			prompts = append(prompts, p)
		case "tool":
			p := devinPrompt{Source: 4, Content: msg.StringContent(), ToolCallID: msg.ToolCallId}
			prompts = append(prompts, p)
		}
	}
	if len(prompts) == 0 {
		return nil, errors.New("devin subscription channel: request has no usable messages")
	}
	if prompts[len(prompts)-1].Source != 1 && prompts[len(prompts)-1].Source != 4 {
		return nil, errors.New("devin subscription channel: last message must be user or tool")
	}

	var tools []devinTool
	for _, t := range request.Tools {
		if t.Type != "" && t.Type != "function" && t.Type != "custom" {
			continue
		}
		name := t.Function.Name
		if name == "" {
			continue
		}
		tools = append(tools, devinTool{
			Name:        name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	maxTokens := int(request.GetMaxTokens())
	var temperature *float64
	if request.Temperature != nil {
		v := *request.Temperature
		temperature = &v
	}

	// Session affinity: pin cascade/session ids to the client cache key when
	// provided so repeat turns share the upstream prompt cache.
	var sessionID, cascadeID string
	cacheKey := strings.TrimSpace(request.PromptCacheKey)
	if info != nil && cacheKey == "" {
		cacheKey = fmt.Sprintf("%d|%s|%d", info.ChannelId, info.UpstreamModelName, info.UserId)
	}
	if cacheKey != "" {
		sessionID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("devin/"+cacheKey)).String()
		cascadeID = sessionID
	}

	return &devinUpstreamRequest{
		Model:       modelUID,
		System:      strings.Join(systemParts, "\n\n"),
		Prompts:     prompts,
		Tools:       tools,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		SessionID:   sessionID,
		CascadeID:   cascadeID,
	}, nil
}

func (a *Adaptor) ConvertOpenAIRequest(_ *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return openAIRequestToDevin(request, info)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	aiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return openAIRequestToDevin(aiRequest, info)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, &request)
	if err != nil {
		return nil, err
	}
	aiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return openAIRequestToDevin(aiRequest, info)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	body, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, err
	}
	var upReq devinUpstreamRequest
	if err := common.Unmarshal(body, &upReq); err != nil {
		return nil, fmt.Errorf("devin subscription channel: invalid converted request: %w", err)
	}

	cred, err := service.ParseDevinCredential(info.ApiKey)
	if err != nil {
		return nil, err
	}

	payload := buildGetChatMessageRequest(
		cred.AccessToken(),
		upReq.Model,
		upReq.System,
		upReq.Prompts,
		upReq.Tools,
		upReq.Temperature,
		upReq.MaxTokens,
		upReq.SessionID,
		upReq.CascadeID,
	)

	url, err := a.GetRequestURL(info)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(wrapConnectEnvelope(connectFlagData, payload)))
	if err != nil {
		return nil, err
	}
	token := cred.AccessToken()
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Basic "+token+"-"+token)
	req.Header.Set("Sentry-Trace", generateSentryTrace())
	req.Header.Set("Accept", "application/connect+proto")
	// Native devin-cli suppresses the User-Agent header on the wire.
	req.Header["User-Agent"] = []string{""}

	client, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}

	// Peek the first frame: an immediate end-stream trailer carrying an
	// upstream error (bad model, quota exhausted) must surface as an HTTP
	// error rather than an empty completion.
	firstFlag, firstPayload, err := readConnectFrame(resp.Body)
	if err != nil && err != io.EOF {
		resp.Body.Close()
		return nil, fmt.Errorf("devin subscription channel: read upstream stream: %w", err)
	}
	if err == nil && firstFlag&connectFlagEndStream != 0 {
		if statusCode, trailerErr := parseDevinTrailerError(firstPayload); trailerErr != nil {
			resp.Body.Close()
			resp.StatusCode = statusCode
			resp.Status = fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode))
			errBody, _ := common.Marshal(map[string]any{
				"error": map[string]any{
					"message": trailerErr.Error(),
					"type":    "upstream_error",
					"code":    "devin_upstream",
				},
			})
			resp.Body = io.NopCloser(bytes.NewReader(errBody))
			resp.Header.Set("Content-Type", "application/json")
			return resp, nil
		}
	}

	if info.IsStream {
		// Translate Connect frames into OpenAI chunk SSE on the fly.
		resp.Body = streamDevinAsOpenAISSE(resp.Body, firstFlag, firstPayload, err == io.EOF, info.UpstreamModelName)
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Del("Content-Length")
		return resp, nil
	}

	// Non-stream: aggregate the full frame stream eagerly.
	jsonBody, closeErr := aggregateDevinResponse(resp.Body, firstFlag, firstPayload, err == io.EOF, info.UpstreamModelName)
	resp.Body.Close()
	if closeErr != nil {
		return nil, closeErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(jsonBody))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Del("Content-Length")
	return resp, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.RelayMode == relayconstant.RelayModeResponses {
		if info.IsStream {
			return openai.OaiChatToResponsesStreamHandler(c, info, resp)
		}
		return openai.OaiChatToResponsesHandler(c, info, resp)
	}
	return a.Adaptor.DoResponse(c, resp, info)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

// --- upstream frame stream -> OpenAI protocol translation ---

// streamDevinAsOpenAISSE spawns a translator goroutine feeding an io.Pipe with
// `data: {chat.completion.chunk}\n\n` lines terminated by `data: [DONE]`.
// `firstFlag`/`firstPayload` carry the eagerly-peeked first frame (may be a
// data frame, a clean end-stream, or empty when the stream hit EOF).
func streamDevinAsOpenAISSE(upstream io.ReadCloser, firstFlag byte, firstPayload []byte, hitEOF bool, model string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		var writeErr error
		defer func() {
			upstream.Close()
			if writeErr != nil {
				pw.CloseWithError(writeErr)
			} else {
				pw.Close()
			}
		}()

		chatID := "chatcmpl-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
		created := time.Now().Unix()
		toolIndexByCall := make(map[string]int)
		toolIndexCount := 0
		sentRole := false
		var contentBuf, thinkingBuf utf8SplitBuffer

		emit := func(chunk *dto.ChatCompletionsStreamResponse) bool {
			data, err := common.Marshal(chunk)
			if err != nil {
				return true
			}
			if _, err := pw.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
				writeErr = err
				return false
			}
			return true
		}
		newChunk := func() *dto.ChatCompletionsStreamResponse {
			return &dto.ChatCompletionsStreamResponse{
				Id:      chatID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []dto.ChatCompletionsStreamResponseChoice{{Index: 0}},
			}
		}
		emitDelta := func(mut func(delta *dto.ChatCompletionsStreamResponseChoiceDelta)) bool {
			chunk := newChunk()
			mut(&chunk.Choices[0].Delta)
			return emit(chunk)
		}

		if !hitEOF && processDevinPayload(firstFlag, firstPayload, newChunk, emit, emitDelta, pw, &toolIndexByCall, &toolIndexCount, &sentRole, &contentBuf, &thinkingBuf) {
			return
		}
		if hitEOF {
			goto done
		}
		for {
			flag, payload, err := readConnectFrame(upstream)
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					writeErr = emitUpstreamError(pw, err.Error())
					return
				}
				break
			}
			if processDevinPayload(flag, payload, newChunk, emit, emitDelta, pw, &toolIndexByCall, &toolIndexCount, &sentRole, &contentBuf, &thinkingBuf) {
				return
			}
			if flag&connectFlagEndStream != 0 {
				break
			}
		}
	done:
		// Flush trailing partial UTF-8 bytes defensively.
		if tail := thinkingBuf.feed(nil); tail != "" {
			if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetReasoningContent(tail) }) {
				return
			}
		}
		if tail := contentBuf.feed(nil); tail != "" {
			if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetContentString(tail) }) {
				return
			}
		}
		if _, err := pw.Write([]byte("data: [DONE]\n\n")); err != nil {
			writeErr = err
		}
	}()
	return pr
}

// processDevinPayload handles one frame: returns true when the stream must
// terminate (write error or trailer error already emitted).
func processDevinPayload(
	flag byte,
	payload []byte,
	newChunk func() *dto.ChatCompletionsStreamResponse,
	emit func(*dto.ChatCompletionsStreamResponse) bool,
	emitDelta func(func(*dto.ChatCompletionsStreamResponseChoiceDelta)) bool,
	pw io.Writer,
	toolIndexByCall *map[string]int,
	toolIndexCount *int,
	sentRole *bool,
	contentBuf, thinkingBuf *utf8SplitBuffer,
) bool {
	if flag&connectFlagEndStream != 0 {
		return false
	}
	frame, err := parseDevinFrame(payload)
	if err != nil {
		return false
	}

	if !*sentRole && (frame.ContentText != "" || frame.ThinkingText != "" || len(frame.ToolCallDeltas) > 0) {
		*sentRole = true
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.Role = "assistant" }) {
			return true
		}
	}

	if thinking := thinkingBuf.feed([]byte(frame.ThinkingText)); thinking != "" {
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetReasoningContent(thinking) }) {
			return true
		}
	}
	if content := contentBuf.feed([]byte(frame.ContentText)); content != "" {
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetContentString(content) }) {
			return true
		}
	}

	for _, tc := range frame.ToolCallDeltas {
		idx, ok := (*toolIndexByCall)[tc.ID]
		if !ok {
			idx = *toolIndexCount
			*toolIndexCount++
			(*toolIndexByCall)[tc.ID] = idx
		}
		call := dto.ToolCallResponse{Index: &idx}
		if tc.ID != "" {
			call.ID = tc.ID
			call.Type = "function"
		}
		call.Function.Name = tc.Name
		call.Function.Arguments = tc.Arguments
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) {
			d.ToolCalls = []dto.ToolCallResponse{call}
		}) {
			return true
		}
	}

	if frame.Usage != nil {
		chunk := newChunk()
		chunk.Choices = []dto.ChatCompletionsStreamResponseChoice{}
		chunk.Usage = devinUsageToOpenAI(frame.Usage)
		if !emit(chunk) {
			return true
		}
	}

	if frame.StopReason != 0 {
		finish := "stop"
		if frame.StopReason == 10 {
			finish = "tool_calls"
		}
		chunk := newChunk()
		chunk.Choices[0].FinishReason = &finish
		if !emit(chunk) {
			return true
		}
	}
	return false
}

// emitUpstreamError writes an OpenAI-shaped error object as the last SSE line.
func emitUpstreamError(w io.Writer, message string) error {
	errObj := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    "devin_upstream",
		},
	}
	data, err := common.Marshal(errObj)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
		return err
	}
	_, err = w.Write([]byte("data: [DONE]\n\n"))
	return err
}

func devinUsageToOpenAI(u *devinUsage) *dto.Usage {
	if u == nil {
		return nil
	}
	promptTokens := u.PromptTokens + u.CachedTokens + u.CacheWriteTokens
	usage := &dto.Usage{
		PromptTokens:     int(promptTokens),
		CompletionTokens: int(u.CompletionTokens),
		TotalTokens:      int(promptTokens + u.CompletionTokens),
	}
	usage.PromptTokensDetails.CachedTokens = int(u.CachedTokens)
	usage.PromptTokensDetails.CacheWriteTokens = int(u.CacheWriteTokens)
	return usage
}

// aggregateDevinResponse drains the upstream frame stream into a single
// OpenAI chat.completion JSON body. firstFlag/firstPayload are the
// eagerly-peeked first frame; hitEOF means the stream ended before any frame.
func aggregateDevinResponse(upstream io.Reader, firstFlag byte, firstPayload []byte, hitEOF bool, model string) ([]byte, error) {
	var contentBuilder, thinkingBuilder strings.Builder
	var contentBuf, thinkingBuf utf8SplitBuffer

	toolCalls := map[int]*dto.ToolCallResponse{}
	toolOrder := []int{}
	toolIndexByCall := make(map[string]int)
	finishReason := "stop"
	var usage *dto.Usage

	consume := func(flag byte, payload []byte) (bool, error) {
		if flag&connectFlagEndStream != 0 {
			if _, trailerErr := parseDevinTrailerError(payload); trailerErr != nil {
				return true, trailerErr
			}
			return true, nil
		}
		frame, err := parseDevinFrame(payload)
		if err != nil {
			return false, nil
		}
		thinkingBuilder.WriteString(thinkingBuf.feed([]byte(frame.ThinkingText)))
		contentBuilder.WriteString(contentBuf.feed([]byte(frame.ContentText)))

		for _, tc := range frame.ToolCallDeltas {
			idx, ok := toolIndexByCall[tc.ID]
			if !ok {
				idx = len(toolOrder)
				toolOrder = append(toolOrder, idx)
				toolIndexByCall[tc.ID] = idx
				call := &dto.ToolCallResponse{ID: tc.ID, Type: "function"}
				toolCalls[idx] = call
			}
			call := toolCalls[idx]
			if tc.Name != "" {
				call.Function.Name = tc.Name
			}
			call.Function.Arguments += tc.Arguments
		}
		if frame.Usage != nil {
			usage = devinUsageToOpenAI(frame.Usage)
		}
		if frame.StopReason == 10 {
			finishReason = "tool_calls"
		}
		return false, nil
	}

	if !hitEOF {
		stop, err := consume(firstFlag, firstPayload)
		if err != nil {
			return nil, err
		}
		if !stop {
			for {
				flag, payload, err := readConnectFrame(upstream)
				if err != nil {
					if err == io.EOF || err == io.ErrUnexpectedEOF {
						break
					}
					return nil, err
				}
				stop, err := consume(flag, payload)
				if err != nil {
					return nil, err
				}
				if stop {
					break
				}
			}
		}
	}
	contentBuilder.WriteString(contentBuf.feed(nil))
	thinkingBuilder.WriteString(thinkingBuf.feed(nil))

	msg := dto.Message{Role: "assistant"}
	msg.SetStringContent(contentBuilder.String())
	if thinking := thinkingBuilder.String(); thinking != "" {
		reasoning := thinking
		msg.ReasoningContent = &reasoning
	}
	if len(toolOrder) > 0 {
		calls := make([]dto.ToolCallResponse, 0, len(toolOrder))
		for _, idx := range toolOrder {
			calls = append(calls, *toolCalls[idx])
		}
		msg.SetToolCalls(calls)
	}

	response := dto.OpenAITextResponse{
		Id:      "chatcmpl-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
		Model:   model,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
	}
	if usage != nil {
		response.Usage = *usage
	}
	return common.Marshal(response)
}
