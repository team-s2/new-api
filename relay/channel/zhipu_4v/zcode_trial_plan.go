package zhipu_4v

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	channelconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
)

// zcodeTrialPlanBaseURL resolves the Anthropic Messages base of a trial plan
// channel: the preset entry, or the endpoint a channel states directly.
func zcodeTrialPlanBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if special, ok := channelconstant.ChannelSpecialBases[trimmed]; ok && special.ClaudeBaseURL != "" {
		return special.ClaudeBaseURL
	}
	return strings.TrimSuffix(trimmed, "/")
}

// zcodeTrialPlanBody releases the plan concurrency slot once the response body
// is closed, which happens whether the response streamed or not.
type zcodeTrialPlanBody struct {
	io.ReadCloser
	channelId int
	once      sync.Once
}

func (body *zcodeTrialPlanBody) Close() error {
	body.once.Do(func() {
		service.ZcodeTrialPlanRelease(body.channelId)
	})
	return body.ReadCloser.Close()
}

// injectZcodeTrialPlanSystem prepends the official client system blocks the
// zcode-plan gateway requires, keeping the caller's system content after them.
// It is idempotent: a request that already starts with the official header is
// returned unchanged, so retries never stack the injected prompt.
func injectZcodeTrialPlanSystem(payload []byte) ([]byte, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return payload, nil
	}

	var body map[string]json.RawMessage
	if err := common.Unmarshal(payload, &body); err != nil {
		return nil, errors.New("zcode trial plan: request body must be a JSON object")
	}

	systemRaw, hasSystem := body["system"]
	if hasSystem && zcodeSystemHasOfficialPrompt(systemRaw) {
		return payload, nil
	}

	headerBlock, err := zcodeTextBlock(zcodeTrialPlanSystemHeader)
	if err != nil {
		return nil, err
	}
	bodyBlock, err := zcodeTextBlock(zcodeTrialPlanSystemBody)
	if err != nil {
		return nil, err
	}
	blocks := []json.RawMessage{headerBlock, bodyBlock}
	if hasSystem {
		blocks = append(blocks, zcodeSystemBlocks(systemRaw)...)
	}

	encoded, err := common.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	body["system"] = encoded
	return common.Marshal(body)
}

func zcodeTextBlock(text string) (json.RawMessage, error) {
	return common.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
}

// zcodeSystemHasOfficialPrompt reports whether the request already carries both
// official blocks, which makes injection a no-op. A request with only the header
// block still gets the body block, because the gateway checks the body text.
func zcodeSystemHasOfficialPrompt(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if common.Unmarshal(raw, &blocks) != nil {
		return false
	}
	return len(blocks) > 1 &&
		blocks[0].Text == zcodeTrialPlanSystemHeader &&
		blocks[1].Text == zcodeTrialPlanSystemBody
}

// zcodeSystemBlocks converts the caller's system field, which Anthropic accepts
// as either a string or a block array, into blocks to append after the official
// ones. A caller system that is already the bare official header is dropped
// rather than repeated.
func zcodeSystemBlocks(raw json.RawMessage) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '[' {
		var blocks []json.RawMessage
		if common.Unmarshal(raw, &blocks) != nil {
			return nil
		}
		return blocks
	}
	if raw[0] == '"' {
		var text string
		if common.Unmarshal(raw, &text) != nil || text == "" || text == zcodeTrialPlanSystemHeader {
			return nil
		}
		block, err := zcodeTextBlock(text)
		if err != nil {
			return nil
		}
		return []json.RawMessage{block}
	}
	return []json.RawMessage{raw}
}

// checkZcodeTrialPlanResponse rejects the gateway's business error envelope.
// The plan gateway answers HTTP 200 with {"code": <non-zero>, "msg": ...} when a
// request is rejected (quota exhausted, model not entitled, risk control), while
// a successful response is either SSE or an Anthropic message object, so the
// error never reaches the client as an empty completion. The status code is
// mapped onto the retryable one so the request fails over to another channel.
func checkZcodeTrialPlanResponse(resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.Body == nil || resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
		return resp, nil
	}

	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadResponseBodyFailed)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	message, statusCode := zcodeTrialPlanEnvelopeError(body)
	if message == "" {
		return resp, nil
	}
	return nil, types.NewErrorWithStatusCode(errors.New(message), types.ErrorCodeBadResponseStatusCode, statusCode)
}

// zcodeTrialPlanEnvelopeError reports the gateway's own error envelope and the
// HTTP status it should surface as. The gateway code identifies the cause:
// 1005 is the plan gate, 3006 the relay model whitelist, and 3012 risk control.
func zcodeTrialPlanEnvelopeError(body []byte) (string, int) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", 0
	}
	var probe struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Type string `json:"type"`
	}
	if err := common.Unmarshal(trimmed, &probe); err != nil {
		return "", 0
	}
	if probe.Code == 0 || probe.Type != "" {
		return "", 0
	}

	statusCode := http.StatusBadGateway
	switch probe.Code {
	case 1005:
		statusCode = http.StatusTooManyRequests
	case 3006, 3012:
		statusCode = http.StatusForbidden
	}
	message := strings.TrimSpace(probe.Msg)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return fmt.Sprintf("zcode trial plan: upstream code %d: %s", probe.Code, message), statusCode
}
