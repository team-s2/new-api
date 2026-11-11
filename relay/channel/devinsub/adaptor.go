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
	Model       string           `json:"model"`
	System      string           `json:"system,omitempty"`
	Prompts     []devinPrompt    `json:"prompts"`
	Tools       []devinTool      `json:"tools,omitempty"`
	ToolChoice  *devinToolChoice `json:"tool_choice,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *uint            `json:"max_tokens,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	CascadeID   string           `json:"cascade_id,omitempty"`
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

// openAIRequestToDevin maps a GeneralOpenAIRequest onto the Devin prompt
// history: system/developer messages merge into the system prompt, user=1,
// assistant=2 (with tool calls + thinking), tool=4 (with tool_call_id).
func openAIRequestToDevin(c *gin.Context, request *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) (*devinUpstreamRequest, error) {
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
						source := part.ToFileSource()
						if source == nil {
							return nil, errors.New("devin subscription channel: image URL is required")
						}
						data, mime, err := service.GetBase64Data(c, source, "fetch image for Devin chat")
						if err != nil {
							return nil, fmt.Errorf("devin subscription channel: load image: %w", err)
						}
						if !strings.HasPrefix(mime, "image/") {
							return nil, errors.New("devin subscription channel: attachment must be an image")
						}
						p.Images = append(p.Images, devinImage{Base64Data: data, MimeType: mime})
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

	toolChoice, err := parseDevinToolChoice(request.ToolChoice, tools)
	if err != nil {
		return nil, err
	}

	maxTokens := request.MaxTokens
	if request.MaxCompletionTokens != nil {
		maxTokens = request.MaxCompletionTokens
	}
	var temperature *float64
	if request.Temperature != nil {
		v := *request.Temperature
		temperature = &v
	}

	// Only an explicit cache key opts into session affinity. Scope it to the
	// tenant, channel and model so unrelated conversations cannot share state.
	var sessionID, cascadeID string
	cacheKey := strings.TrimSpace(request.PromptCacheKey)
	if info != nil && cacheKey != "" {
		cacheKey = fmt.Sprintf("%d|%d|%s|%s", info.UserId, info.ChannelId, info.UpstreamModelName, cacheKey)
		sessionID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("devin/"+cacheKey)).String()
		cascadeID = sessionID
	}

	return &devinUpstreamRequest{
		Model:       modelUID,
		System:      strings.Join(systemParts, "\n\n"),
		Prompts:     prompts,
		Tools:       tools,
		ToolChoice:  toolChoice,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		SessionID:   sessionID,
		CascadeID:   cascadeID,
	}, nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return openAIRequestToDevin(c, request, info)
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
	return openAIRequestToDevin(c, aiRequest, info)
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
	return openAIRequestToDevin(c, aiRequest, info)
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
	modelUID, ok := ResolveModelUID(upReq.Model)
	if !ok {
		return nil, fmt.Errorf("devin subscription channel only serves swe-2 / swe-2-flash family models, got %q", upReq.Model)
	}
	upReq.Model = modelUID

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
		upReq.ToolChoice,
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
	// Metadata contains the credential as well as the Authorization header;
	// never replay the request body to a redirect target.
	requestClient := *client
	requestClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
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
	if err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("devin subscription channel: read upstream stream: %w", err)
	}
	if firstFlag&connectFlagEndStream != 0 {
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
		resp.Body = streamDevinAsOpenAISSE(resp.Body, firstFlag, firstPayload, info)
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Del("Content-Length")
		return resp, nil
	}

	// Non-stream: aggregate the full frame stream eagerly.
	jsonBody, closeErr := aggregateDevinResponse(resp.Body, firstFlag, firstPayload, info)
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

// devinToolCalls retains the active call for continuation deltas that omit an
// ID. An explicit ID always selects the active slot, including revisits to a
// parallel call. ChatToolCall has no index field to use instead.
type devinToolCalls struct {
	calls  []dto.ToolCallResponse
	byID   map[string]int
	active int
}

func (calls *devinToolCalls) apply(delta devinToolCallDelta) (int, error) {
	index := calls.active
	if delta.ID != "" {
		var found bool
		index, found = calls.byID[delta.ID]
		if !found {
			index = len(calls.calls)
			if calls.byID == nil {
				calls.byID = make(map[string]int)
			}
			calls.byID[delta.ID] = index
			calls.calls = append(calls.calls, dto.ToolCallResponse{ID: delta.ID, Type: "function"})
		}
		calls.active = index
	} else if len(calls.calls) == 0 {
		return 0, errors.New("devin upstream: tool-call continuation has no active call")
	}
	call := &calls.calls[index]
	if delta.Name != "" {
		call.Function.Name = delta.Name
	}
	call.Function.Arguments += delta.Arguments
	return index, nil
}

// --- upstream frame stream -> OpenAI protocol translation ---

// streamDevinAsOpenAISSE spawns a translator goroutine feeding an io.Pipe with
// `data: {chat.completion.chunk}\n\n` lines terminated by `data: [DONE]`.
// `firstFlag`/`firstPayload` carry the eagerly-peeked first frame (may be a
// data frame or a clean end-stream).
func streamDevinAsOpenAISSE(upstream io.ReadCloser, firstFlag byte, firstPayload []byte, info *relaycommon.RelayInfo) io.ReadCloser {
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
		var toolCalls devinToolCalls
		sentRole := false
		var contentBuf, thinkingBuf utf8SplitBuffer

		emit := func(chunk *dto.ChatCompletionsStreamResponse) bool {
			data, err := common.Marshal(chunk)
			if err != nil {
				writeErr = err
				return false
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
				Model:   info.UpstreamModelName,
				Choices: []dto.ChatCompletionsStreamResponseChoice{{Index: 0}},
			}
		}
		emitDelta := func(mut func(delta *dto.ChatCompletionsStreamResponseChoiceDelta)) bool {
			chunk := newChunk()
			mut(&chunk.Choices[0].Delta)
			return emit(chunk)
		}

		var terminalFrame devinFrameResult
		var upstreamErr error
		flag, payload := firstFlag, firstPayload
		for {
			var frame devinFrameResult
			var err error
			if flag&connectFlagEndStream != 0 {
				_, err = parseDevinTrailerError(payload)
			} else {
				frame, err = parseDevinFrame(payload)
			}
			if err != nil {
				upstreamErr = err
				break
			}
			if flag&connectFlagEndStream != 0 {
				break
			}
			// Protocol bridges may finalize on usage or finish_reason. Hold both
			// until the Connect trailer confirms the request actually succeeded.
			if frame.Usage != nil {
				terminalFrame.Usage = frame.Usage
				frame.Usage = nil
			}
			if frame.StopReason != 0 {
				if _, err := devinFinishReason(frame.StopReason); err != nil {
					upstreamErr = err
					break
				}
				terminalFrame.StopReason = frame.StopReason
				frame.StopReason = 0
			}
			if err := processDevinPayload(frame, newChunk, emit, emitDelta, &toolCalls, &sentRole, &contentBuf, &thinkingBuf, info); err != nil {
				upstreamErr = err
				break
			}
			flag, payload, err = readConnectFrame(upstream)
			if err != nil {
				upstreamErr = fmt.Errorf("devin stream ended before end-stream trailer: %w", err)
				break
			}
		}
		if upstreamErr != nil {
			// Preserve authoritative usage for partial-output settlement. The
			// handler consumes it without emitting a successful terminal event
			// when the immediately following chunk is an error.
			if terminalFrame.Usage != nil {
				chunk := newChunk()
				chunk.Choices = []dto.ChatCompletionsStreamResponseChoice{}
				chunk.Usage = devinUsageToOpenAI(terminalFrame.Usage, info)
				if !emit(chunk) {
					return
				}
			}
			writeErr = upstreamErr
			_ = emitUpstreamError(pw, upstreamErr.Error())
			return
		}
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
		if err := processDevinPayload(terminalFrame, newChunk, emit, emitDelta, &toolCalls, &sentRole, &contentBuf, &thinkingBuf, info); err != nil {
			writeErr = err
			_ = emitUpstreamError(pw, err.Error())
			return
		}
		if _, err := pw.Write([]byte("data: [DONE]\n\n")); err != nil {
			writeErr = err
		}
	}()
	return pr
}

// processDevinPayload emits one decoded frame, with finish before usage so the
// shared OpenAI handler can apply the client's include_usage preference.
func processDevinPayload(
	frame devinFrameResult,
	newChunk func() *dto.ChatCompletionsStreamResponse,
	emit func(*dto.ChatCompletionsStreamResponse) bool,
	emitDelta func(func(*dto.ChatCompletionsStreamResponseChoiceDelta)) bool,
	toolCalls *devinToolCalls,
	sentRole *bool,
	contentBuf, thinkingBuf *utf8SplitBuffer,
	info *relaycommon.RelayInfo,
) error {
	if !*sentRole && (frame.ContentText != "" || frame.ThinkingText != "" || len(frame.ToolCallDeltas) > 0) {
		*sentRole = true
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.Role = "assistant" }) {
			return io.ErrClosedPipe
		}
	}

	if thinking := thinkingBuf.feed([]byte(frame.ThinkingText)); thinking != "" {
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetReasoningContent(thinking) }) {
			return io.ErrClosedPipe
		}
	}
	if content := contentBuf.feed([]byte(frame.ContentText)); content != "" {
		if !emitDelta(func(d *dto.ChatCompletionsStreamResponseChoiceDelta) { d.SetContentString(content) }) {
			return io.ErrClosedPipe
		}
	}

	for _, tc := range frame.ToolCallDeltas {
		idx, err := toolCalls.apply(tc)
		if err != nil {
			return err
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
			return io.ErrClosedPipe
		}
	}

	if frame.StopReason != 0 {
		finish, err := devinFinishReason(frame.StopReason)
		if err != nil {
			return err
		}
		chunk := newChunk()
		chunk.Choices[0].FinishReason = &finish
		if !emit(chunk) {
			return io.ErrClosedPipe
		}
	}
	if frame.Usage != nil {
		chunk := newChunk()
		chunk.Choices = []dto.ChatCompletionsStreamResponseChoice{}
		chunk.Usage = devinUsageToOpenAI(frame.Usage, info)
		if !emit(chunk) {
			return io.ErrClosedPipe
		}
	}

	return nil
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
	return nil
}

func devinUsageToOpenAI(u *devinUsage, info *relaycommon.RelayInfo) *dto.Usage {
	if u == nil {
		return nil
	}
	promptTokens, promptClamp := common.QuotaFromFloatChecked(float64(u.PromptTokens) + float64(u.CachedTokens) + float64(u.CacheWriteTokens))
	totalTokens, totalClamp := common.QuotaFromFloatChecked(float64(promptTokens) + float64(u.CompletionTokens))
	if info != nil && info.QuotaClamp == nil {
		for _, clamp := range []*common.QuotaClamp{u.QuotaClamp, promptClamp, totalClamp} {
			if clamp != nil {
				info.QuotaClamp = clamp
				break
			}
		}
	}
	usage := &dto.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      totalTokens,
	}
	usage.PromptTokensDetails.CachedTokens = u.CachedTokens
	usage.PromptTokensDetails.CacheWriteTokens = u.CacheWriteTokens
	return usage
}

// aggregateDevinResponse drains the upstream frame stream into a single
// OpenAI chat.completion JSON body. firstFlag/firstPayload are the
// eagerly-peeked first frame. A valid Connect stream must end with a trailer.
func aggregateDevinResponse(upstream io.Reader, firstFlag byte, firstPayload []byte, info *relaycommon.RelayInfo) ([]byte, error) {
	var contentBuilder, thinkingBuilder strings.Builder
	var contentBuf, thinkingBuf utf8SplitBuffer

	var toolCalls devinToolCalls
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
			return false, err
		}
		thinkingBuilder.WriteString(thinkingBuf.feed([]byte(frame.ThinkingText)))
		contentBuilder.WriteString(contentBuf.feed([]byte(frame.ContentText)))

		for _, tc := range frame.ToolCallDeltas {
			if _, err := toolCalls.apply(tc); err != nil {
				return false, err
			}
		}
		if frame.Usage != nil {
			usage = devinUsageToOpenAI(frame.Usage, info)
		}
		if frame.StopReason != 0 {
			finishReason, err = devinFinishReason(frame.StopReason)
			if err != nil {
				return false, err
			}
		}
		return false, nil
	}

	flag, payload := firstFlag, firstPayload
	for {
		stop, err := consume(flag, payload)
		if err != nil {
			return nil, err
		}
		if stop {
			break
		}
		flag, payload, err = readConnectFrame(upstream)
		if err != nil {
			return nil, fmt.Errorf("devin stream ended before end-stream trailer: %w", err)
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
	if len(toolCalls.calls) > 0 {
		msg.SetToolCalls(toolCalls.calls)
	}

	response := dto.OpenAITextResponse{
		Id:      "chatcmpl-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
		Model:   info.UpstreamModelName,
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
