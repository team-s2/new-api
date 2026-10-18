package antigravity

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const defaultAntigravityProjectID = "bamboo-precept-lgxtn"

type Adaptor struct {
	gemini.Adaptor
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(info.ChannelBaseUrl), "/")
	if baseURL == "" {
		baseURL = service.DefaultAntigravityBaseURL
	}
	if info.IsStream {
		return baseURL + "/v1internal:streamGenerateContent?alt=sse", nil
	}
	return baseURL + "/v1internal:generateContent", nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	accessToken, err := service.ExtractAntigravityAccessToken(info.ApiKey)
	if err != nil {
		return err
	}
	service.ApplyAntigravityHeaders(*req, accessToken, info.IsStream)
	return nil
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	converted, err := a.Adaptor.ConvertGeminiRequest(c, info, request)
	if err != nil {
		return nil, err
	}
	return wrapAntigravityRequest(info, converted)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.ClaudeRequest) (any, error) {
	converted, err := a.Adaptor.ConvertClaudeRequest(c, info, req)
	if err != nil {
		return nil, err
	}
	return wrapAntigravityRequest(info, converted)
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	converted, err := a.Adaptor.ConvertOpenAIRequest(c, info, request)
	if err != nil {
		return nil, err
	}
	return wrapAntigravityRequest(info, converted)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	converted, err := a.Adaptor.ConvertOpenAIResponsesRequest(c, info, request)
	if err != nil {
		return nil, err
	}
	return wrapAntigravityRequest(info, converted)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	resp, err := channel.DoApiRequest(a, c, info, requestBody)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp, nil
	}

	if info.IsStream {
		resp.Body = newAntigravitySSEUnwrapReader(resp.Body)
		resp.ContentLength = -1
		return resp, nil
	}

	rawBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("failed to read antigravity response body: %w", readErr)
	}
	unwrapped := unwrapAntigravityJSONPayload(rawBody)
	resp.Body = io.NopCloser(bytes.NewReader(unwrapped))
	resp.ContentLength = int64(len(unwrapped))
	return resp, nil
}

func wrapAntigravityRequest(info *relaycommon.RelayInfo, converted any) (any, error) {
	rawBytes, err := common.Marshal(converted)
	if err != nil {
		return nil, err
	}
	reqMap := make(map[string]any)
	if err := common.Unmarshal(rawBytes, &reqMap); err != nil {
		return nil, err
	}

	delete(reqMap, "safetySettings")
	delete(reqMap, "model")
	reqMap["sessionId"] = deriveAntigravitySessionID(info)

	upstreamModel := alias2ModelName(info.UpstreamModelName)
	if strings.Contains(strings.ToLower(upstreamModel), "claude") {
		toolConfig, _ := reqMap["toolConfig"].(map[string]any)
		if toolConfig == nil {
			toolConfig = make(map[string]any)
		}
		fnConfig, _ := toolConfig["functionCallingConfig"].(map[string]any)
		if fnConfig == nil {
			fnConfig = make(map[string]any)
		}
		fnConfig["mode"] = "VALIDATED"
		toolConfig["functionCallingConfig"] = fnConfig
		reqMap["toolConfig"] = toolConfig
	}

	projectID := defaultAntigravityProjectID
	if parsedKey, ok := service.ParseAntigravityOAuthKey(info.ApiKey); ok && parsedKey.ProjectID != "" {
		projectID = parsedKey.ProjectID
	}

	return map[string]any{
		"project":     projectID,
		"requestId":   "agent-" + common.GetUUID(),
		"requestType": "agent",
		"userAgent":   "antigravity",
		"model":       upstreamModel,
		"request":     reqMap,
	}, nil
}

func deriveAntigravitySessionID(info *relaycommon.RelayInfo) string {
	seed := "antigravity"
	if info != nil {
		seed = fmt.Sprintf("antigravity:%d:%d:%s", info.ChannelId, info.UserId, info.OriginModelName)
	}
	sum := sha256.Sum256([]byte(seed))
	n := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7FFFFFFFFFFFFFFF)
	if n == 0 {
		n = 1
	}
	return "-" + strconv.FormatInt(n, 10)
}

func unwrapAntigravityJSONPayload(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return raw
	}
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := common.Unmarshal(trimmed, &envelope); err != nil {
		return raw
	}
	inner := bytes.TrimSpace(envelope.Response)
	if len(inner) == 0 || inner[0] != '{' {
		return raw
	}
	return inner
}

type antigravitySSEUnwrapReader struct {
	closer io.Closer
	reader *bufio.Reader
	buf    bytes.Buffer
	eof    bool
}

func newAntigravitySSEUnwrapReader(rc io.ReadCloser) io.ReadCloser {
	return &antigravitySSEUnwrapReader{
		closer: rc,
		reader: bufio.NewReader(rc),
	}
}

func (r *antigravitySSEUnwrapReader) Read(p []byte) (int, error) {
	for r.buf.Len() == 0 {
		if r.eof {
			return 0, io.EOF
		}
		line, err := r.reader.ReadBytes('\n')
		if len(line) > 0 {
			r.buf.Write(transformAntigravitySSELine(line))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.eof = true
				break
			}
			return 0, err
		}
	}
	return r.buf.Read(p)
}

func (r *antigravitySSEUnwrapReader) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

func transformAntigravitySSELine(line []byte) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	payload, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return line
	}
	unwrapped := unwrapAntigravityJSONPayload(payload)
	if bytes.Equal(unwrapped, payload) {
		return line
	}
	out := make([]byte, 0, len("data: ")+len(unwrapped)+1)
	out = append(out, "data: "...)
	out = append(out, unwrapped...)
	out = append(out, '\n')
	return out
}
