package groksub

import (
	"bytes"
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

// Adaptor relays to the Grok CLI gateway over the OpenAI Responses protocol.
// Only /v1/responses traffic is served natively; chat/completions and Claude
// messages requests are bridged by the host (see ShouldForceChatViaResponses).
type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	path := ""
	switch info.RelayMode {
	case relayconstant.RelayModeResponses:
		path = "/responses"
	default:
		return "", errors.New("grok subscription channel: endpoint not supported")
	}
	return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, path, info.ChannelType), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)

	oauthKey, err := ParseOAuthKey(strings.TrimSpace(info.ApiKey))
	if err != nil {
		return err
	}
	if strings.TrimSpace(oauthKey.AccessToken) == "" {
		return errors.New("grok subscription channel: access_token is required")
	}
	req.Set("Authorization", "Bearer "+strings.TrimSpace(oauthKey.AccessToken))

	// Stamp the pinned CLI identity. The gateway fingerprints the client
	// string, so inbound client UAs must never be forwarded.
	req.Set("User-Agent", CLIUserAgent())
	req.Set("x-grok-client-version", cliClientVersion)
	req.Set("x-grok-client-identifier", cliClientIdentifier)
	req.Set("X-Grok-Client-Mode", cliClientMode)
	if strings.Contains(strings.ToLower(req.Get("Host")), cliProxyHost) ||
		strings.Contains(strings.ToLower(info.ChannelBaseUrl), cliProxyHost) {
		req.Set("X-XAI-Token-Auth", cliTokenAuth)
	}

	req.Set("Content-Type", "application/json")
	req.Set("Accept", "application/json, text/event-stream")
	return nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if request.Model == "" && info != nil {
		request.Model = info.UpstreamModelName
	}
	request.Model = ResolveModelID(request.Model)
	if info != nil {
		info.UpstreamModelName = request.Model
	}

	// Fields the CLI gateway rejects or must not receive.
	request.Metadata = nil
	request.PromptCacheRetention = nil
	request.SafetyIdentifier = nil
	request.Include = nil
	request.Store = json.RawMessage("false")

	sanitizeReasoning(&request, request.Model)
	sanitizeInputNulls(&request)
	sanitizeTools(&request)

	return request, nil
}

// sanitizeInputNulls strips explicit JSON nulls from the input array. The
// gateway's untagged input decoder rejects them with 422.
func sanitizeInputNulls(request *dto.OpenAIResponsesRequest) {
	if len(request.Input) == 0 {
		return
	}
	var items []json.RawMessage
	if err := common.Unmarshal(request.Input, &items); err != nil {
		return
	}
	cleaned := make([]json.RawMessage, 0, len(items))
	changed := false
	for _, item := range items {
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
			changed = true
			continue
		}
		cleanedItem, ok := stripNullsRaw(item)
		if !ok {
			cleaned = append(cleaned, item)
			continue
		}
		changed = true
		cleaned = append(cleaned, cleanedItem)
	}
	if !changed {
		return
	}
	if encoded, err := common.Marshal(cleaned); err == nil {
		request.Input = encoded
	}
}

// stripNullsRaw recursively removes null-valued object members from raw JSON.
// Returns the rewritten JSON and whether anything changed.
func stripNullsRaw(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := common.Unmarshal(trimmed, &obj); err != nil {
			return raw, false
		}
		changed := false
		for key, value := range obj {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				delete(obj, key)
				changed = true
				continue
			}
			if nested, ok := stripNullsRaw(value); ok {
				obj[key] = nested
				changed = true
			}
		}
		if !changed {
			return raw, false
		}
		encoded, err := common.Marshal(obj)
		if err != nil {
			return raw, false
		}
		return encoded, true
	case '[':
		var arr []json.RawMessage
		if err := common.Unmarshal(trimmed, &arr); err != nil {
			return raw, false
		}
		changed := false
		out := make([]json.RawMessage, 0, len(arr))
		for _, item := range arr {
			if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
				// Keep array positions stable: null array elements become
				// empty objects only when they must remain addressable.
				changed = true
				continue
			}
			if nested, ok := stripNullsRaw(item); ok {
				out = append(out, nested)
				changed = true
				continue
			}
			out = append(out, item)
		}
		if !changed {
			return raw, false
		}
		encoded, err := common.Marshal(out)
		if err != nil {
			return raw, false
		}
		return encoded, true
	}
	return raw, false
}

// reasoningEffortSupported reports whether the model accepts a reasoning
// effort setting; unknown model families must not send the field.
func reasoningEffortSupported(model string) bool {
	for _, prefix := range []string{"grok-4.5", "grok-4.6", "grok-4.7", "grok-4.3", "grok-3-mini"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	switch model {
	case "grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20-multi-agent-0309":
		return true
	}
	return false
}

func sanitizeReasoning(request *dto.OpenAIResponsesRequest, model string) {
	if request.Reasoning == nil {
		return
	}
	if strings.HasPrefix(model, "grok-composer") || model == "grok-composer-2.5-fast" {
		request.Reasoning = nil
		return
	}
	if !reasoningEffortSupported(model) {
		request.Reasoning = nil
		return
	}
	effort := strings.ToLower(strings.TrimSpace(request.Reasoning.Effort))
	switch effort {
	case "":
	case "low", "medium", "high":
		request.Reasoning.Effort = effort
	case "minimal":
		request.Reasoning.Effort = "low"
	case "xhigh", "extrahigh":
		if model == "grok-4.6" || model == "grok-4.6-latest" || model == "grok-4.7" || model == "grok-4.7-latest" {
			request.Reasoning.Effort = "xhigh"
		} else {
			request.Reasoning.Effort = "high"
		}
	case "max", "ultra":
		request.Reasoning.Effort = "high"
	default:
		request.Reasoning = nil
		return
	}
}

// sanitizeTools filters tool declarations to the types the CLI gateway
// supports and repairs function schemas it rejects.
func sanitizeTools(request *dto.OpenAIResponsesRequest) {
	if len(request.Tools) == 0 {
		request.ToolChoice = nil
		request.ParallelToolCalls = nil
		return
	}
	var tools []json.RawMessage
	if err := common.Unmarshal(request.Tools, &tools); err != nil {
		return
	}
	kept := make([]json.RawMessage, 0, len(tools))
	declared := map[string]bool{}
	for _, tool := range tools {
		var t map[string]any
		if err := common.Unmarshal(tool, &t); err != nil {
			kept = append(kept, tool)
			continue
		}
		toolType, _ := t["type"].(string)
		if !grokSupportedToolType(toolType) {
			continue
		}
		if toolType == "function" {
			fixGrokFunctionTool(t)
			if name, _ := t["name"].(string); name != "" {
				declared[name] = true
			} else if fn, ok := t["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					declared[name] = true
				}
			}
		}
		encoded, err := common.Marshal(t)
		if err != nil {
			continue
		}
		kept = append(kept, encoded)
	}
	if len(kept) == 0 {
		request.Tools = nil
		request.ToolChoice = nil
		request.ParallelToolCalls = nil
		return
	}
	encoded, err := common.Marshal(kept)
	if err == nil {
		request.Tools = encoded
	}
	dropDanglingToolChoice(request, declared)
}

var grokSupportedToolTypes = map[string]bool{
	"code_execution":     true,
	"code_interpreter":   true,
	"collections_search": true,
	"file_search":        true,
	"function":           true,
	"mcp":                true,
	"shell":              true,
	"web_search":         true,
	"x_search":           true,
}

func grokSupportedToolType(toolType string) bool {
	return grokSupportedToolTypes[strings.ToLower(strings.TrimSpace(toolType))]
}

// fixGrokFunctionTool repairs function tool declarations the gateway rejects:
// missing parameters, or non-object anyOf/oneOf union roots.
func fixGrokFunctionTool(t map[string]any) {
	fn, _ := t["function"].(map[string]any)
	if fn == nil {
		return
	}
	params, ok := fn["parameters"]
	if !ok || params == nil {
		fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		return
	}
	obj, ok := params.(map[string]any)
	if !ok {
		fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		return
	}
	if root, _ := obj["type"].(string); root == "object" {
		return
	}
	if _, hasAnyOf := obj["anyOf"]; hasAnyOf {
		fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
		return
	}
	if _, hasOneOf := obj["oneOf"]; hasOneOf {
		fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
		return
	}
	fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
}

func dropDanglingToolChoice(request *dto.OpenAIResponsesRequest, declared map[string]bool) {
	if len(request.ToolChoice) == 0 {
		return
	}
	var choice any
	if err := common.Unmarshal(request.ToolChoice, &choice); err != nil {
		return
	}
	switch v := choice.(type) {
	case string:
		if v == "auto" || v == "none" || v == "required" {
			return
		}
		request.ToolChoice = nil
	case map[string]any:
		toolType, _ := v["type"].(string)
		if toolType != "function" {
			request.ToolChoice = nil
			return
		}
		name, _ := v["name"].(string)
		if name == "" {
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
		}
		if name == "" || !declared[name] {
			request.ToolChoice = nil
		}
	default:
		request.ToolChoice = nil
	}
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return nil, errors.New("grok subscription channel: /v1/chat/completions is bridged by the host, not the adaptor")
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, errors.New("grok subscription channel: /v1/messages endpoint not supported")
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

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case relayconstant.RelayModeResponses:
		// The CLI gateway injects billing ping frames that strict Responses
		// clients reject; filter them out before the shared handlers scan
		// the body.
		resp.Body = newGrokPingFilterBody(resp.Body)
		if info.IsStream {
			usage, err = openai.OaiResponsesStreamHandler(c, info, resp)
		} else {
			usage, err = openai.OaiResponsesHandler(c, info, resp)
		}
		if err != nil {
			return nil, err
		}
		return adaptGrokUsage(usage), nil
	default:
		return nil, types.NewError(errors.New("grok subscription channel: endpoint not supported"), types.ErrorCodeInvalidRequest)
	}
}

// GetModelList / GetChannelName satisfy the adaptor interface for dashboards.
func (a *Adaptor) GetModelList() []string { return ModelList }
func (a *Adaptor) GetChannelName() string { return ChannelName }
