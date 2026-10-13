package codex

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The Codex chatgpt.com backend expects the native codex-tui request shape.
// The constants below mirror the fields the CLI always sends.
var (
	codexImageGenerationTool      = []byte(`{"type":"image_generation","output_format":"png"}`)
	codexImageGenerationToolArray = []byte(`[{"type":"image_generation","output_format":"png"}]`)
	codexIncludeReasoningOnly     = json.RawMessage(`["reasoning.encrypted_content"]`)
	codexIncludeWithSources       = json.RawMessage(`["reasoning.encrypted_content","web_search_call.action.sources"]`)
)

// resolveCodexSessionID returns the stable session identity for one request:
// the client's session header first, then the body prompt_cache_key, then a
// UUID derived from the API token. The same value is written to the upstream
// prompt_cache_key and session header so prompt caching and the backend's
// session tracking stay aligned across requests.
func resolveCodexSessionID(c *gin.Context, info *relaycommon.RelayInfo) string {
	if c != nil && c.Request != nil {
		for _, name := range []string{"Session_id", "Session-Id"} {
			if value := strings.TrimSpace(c.Request.Header.Get(name)); value != "" {
				return value
			}
		}
		if storage, err := common.GetBodyStorage(c); err == nil {
			if body, err := storage.Bytes(); err == nil && len(body) > 0 {
				if value := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); value != "" {
					return value
				}
			}
		}
	}
	if info != nil {
		return CodexSessionUUID(info.TokenKey)
	}
	return ""
}

// isCodexResponsesLiteRequest recognizes the native lite header and its
// websocket client_metadata mirror, matching the upstream lite dialect.
func isCodexResponsesLiteRequest(c *gin.Context, request *dto.OpenAIResponsesRequest) bool {
	if c != nil && c.Request != nil {
		if strings.EqualFold(strings.TrimSpace(c.Request.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite")), "true") {
			return true
		}
	}
	if request != nil && len(request.ClientMetadata) > 0 {
		value := gjson.GetBytes(request.ClientMetadata, "ws_request_header_x_openai_internal_codex_responses_lite")
		if value.Type == gjson.True || value.Type == gjson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true") {
			return true
		}
	}
	return false
}

// codexNativePassThroughHeaders are the native codex-tui headers copied from
// the incoming client request to the upstream request when present, matching
// the header set the affinity template already forwards. HTTP header lookup
// is case-insensitive, so the lower-case codex spellings cover both forms.
// https://github.com/openai/codex/commit/7c7b4861d88960f7e3bd5b7f30f8351be666dd84
// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
var codexNativePassThroughHeaders = []string{
	"Version",
	"Thread_id",
	"Thread-Id",
	"X-Client-Request-Id",
	"X-Codex-Beta-Features",
	"X-Codex-Turn-Metadata",
	"X-Codex-Turn-State",
	"X-Codex-Window-Id",
	"X-Openai-Internal-Codex-Responses-Lite",
	"X-Responsesapi-Include-Timing-Metrics",
}

// passthroughCodexNativeHeaders copies the native codex-tui headers from the
// incoming client request onto the upstream request when the client sent
// them. Values are only set when non-empty; existing upstream values win.
func passthroughCodexNativeHeaders(c *gin.Context, req *http.Header) {
	if c == nil || c.Request == nil || req == nil {
		return
	}
	for _, name := range codexNativePassThroughHeaders {
		value := strings.TrimSpace(c.Request.Header.Get(name))
		if value == "" {
			continue
		}
		if existing := strings.TrimSpace(req.Get(name)); existing != "" {
			continue
		}
		req.Set(name, value)
	}
}

// canonicalCodexInclude rewrites include to the values the native CLI sends:
// encrypted reasoning content, plus web-search sources when the request uses
// web search. Other include values are not accepted by the Codex backend.
func canonicalCodexInclude(include, tools json.RawMessage) json.RawMessage {
	if includeRequestsSources(include) || toolsContainWebSearch(tools) {
		return codexIncludeWithSources
	}
	return codexIncludeReasoningOnly
}

func includeRequestsSources(include json.RawMessage) bool {
	parsed := gjson.ParseBytes(include)
	if !parsed.IsArray() {
		return false
	}
	for _, value := range parsed.Array() {
		if value.Type == gjson.String && value.String() == "web_search_call.action.sources" {
			return true
		}
	}
	return false
}

func toolsContainWebSearch(tools json.RawMessage) bool {
	parsed := gjson.ParseBytes(tools)
	if !parsed.IsArray() {
		return false
	}
	for _, tool := range parsed.Array() {
		switch tool.Get("type").String() {
		case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
			return true
		}
	}
	return false
}

func codexToolsExist(tools json.RawMessage) bool {
	parsed := gjson.ParseBytes(tools)
	return parsed.IsArray() && len(parsed.Array()) > 0
}

// ensureCodexImageGenerationTool appends the image_generation tool the CLI
// always carries, unless an image tool is already present.
func ensureCodexImageGenerationTool(tools json.RawMessage) json.RawMessage {
	parsed := gjson.ParseBytes(tools)
	if !parsed.IsArray() {
		return json.RawMessage(append([]byte(nil), codexImageGenerationToolArray...))
	}
	for _, tool := range parsed.Array() {
		if tool.Get("type").String() == "image_generation" || isImageGenerationFunctionTool(tool) {
			return tools
		}
	}
	updated, err := sjson.SetRawBytes(tools, "-1", codexImageGenerationTool)
	if err != nil {
		return tools
	}
	return updated
}

func isImageGenerationFunctionTool(tool gjson.Result) bool {
	switch tool.Get("type").String() {
	case "function":
		return tool.Get("name").String() == "image_gen.imagegen"
	case "namespace":
		if tool.Get("name").String() != "image_gen" {
			return false
		}
		for _, nested := range tool.Get("tools").Array() {
			if nested.Get("type").String() == "function" && nested.Get("name").String() == "imagegen" {
				return true
			}
		}
	}
	return false
}

// convertSystemRoleToDeveloper rewrites system roles in the input array to
// developer; the Codex backend rejects the system role in input items.
func convertSystemRoleToDeveloper(input json.RawMessage) json.RawMessage {
	parsed := gjson.ParseBytes(input)
	if !parsed.IsArray() {
		return input
	}
	items := parsed.Array()
	updated := input
	for i, item := range items {
		if item.IsObject() && item.Get("role").String() == "system" {
			if next, err := sjson.SetBytes(updated, strconv.Itoa(i)+".role", "developer"); err == nil {
				updated = next
			}
		}
	}
	return updated
}
