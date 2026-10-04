package groksub

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// patchRequestBody rewrites a decoded Responses request into the subset the
// Grok CLI gateway accepts, in the same order as sub2api's
// patchGrokResponsesBodyWithClientTools: client tools are lowered before the
// supported-type filter so custom/tool_search/namespace tools survive it.
func patchRequestBody(payload map[string]any, model string) (*clientToolMapping, error) {
	promoteAdditionalTools(payload)
	mapping, err := adaptClientTools(payload)
	if err != nil {
		return nil, err
	}

	for _, field := range []string{"prompt_cache_retention", "safety_identifier", "metadata"} {
		delete(payload, field)
	}
	normalizeReasoning(payload, model)
	if strings.EqualFold(model, "grok-4.5") {
		// grok-4.5 rejects sampling penalties and stop sequences.
		for _, field := range []string{"presence_penalty", "presencePenalty", "frequency_penalty", "frequencyPenalty", "stop"} {
			delete(payload, field)
		}
	}
	if strings.HasPrefix(strings.ToLower(model), "grok-4.20") {
		delete(payload, "logprobs")
		delete(payload, "top_logprobs")
	}
	deleteFieldRecursive(payload, "external_web_access")

	convertCompactInputs(payload)
	if input, ok := payload["input"]; ok {
		payload["input"] = sanitizeModelInput(input)
	}
	stripRedundantViewImageTool(payload)
	if input, ok := payload["input"]; ok {
		stripInputNulls(input)
	}
	sanitizeTools(payload)
	return mapping, nil
}

// promoteAdditionalTools lifts tools from Codex's private additional_tools
// input carrier (which xAI rejects) to the top level, appending tools not
// already declared in carrier order.
func promoteAdditionalTools(payload map[string]any) {
	items, ok := payload["input"].([]any)
	if !ok {
		return
	}
	tools, _ := payload["tools"].([]any)
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		seen[toolDedupKey(tool)] = true
	}
	filtered := make([]any, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(jsonStringValue(item["type"])) != "additional_tools" {
			filtered = append(filtered, rawItem)
			continue
		}
		carried, _ := item["tools"].([]any)
		for _, tool := range carried {
			if key := toolDedupKey(tool); !seen[key] {
				seen[key] = true
				tools = append(tools, tool)
			}
		}
	}
	if len(filtered) == len(items) {
		return
	}
	payload["input"] = filtered
	if len(tools) > 0 {
		payload["tools"] = tools
	}
}

func toolDedupKey(raw any) string {
	if tool, ok := raw.(map[string]any); ok {
		toolType := strings.TrimSpace(jsonStringValue(tool["type"]))
		if name := strings.TrimSpace(jsonStringValue(tool["name"])); toolType != "" && name != "" {
			return "type:" + toolType + "\x00name:" + name
		}
		if label := strings.TrimSpace(jsonStringValue(tool["server_label"])); toolType == "mcp" && label != "" {
			return "type:mcp\x00server_label:" + label
		}
	}
	encoded, _ := common.Marshal(raw)
	return "json:" + string(encoded)
}

// normalizeReasoning maps OpenAI effort levels onto the ones xAI accepts and
// drops the effort for models that reject it. Grok Composer rejects the whole
// reasoning object.
func normalizeReasoning(payload map[string]any, model string) {
	lower := strings.ToLower(model)
	if lower == "grok-composer" || lower == "grok-composer-2.5-fast" || lower == "composer-2.5" {
		delete(payload, "reasoning")
		delete(payload, "reasoning_effort")
		return
	}
	supported := false
	switch lower {
	case "grok-4.5", "grok-4.5-latest", "grok-4.6", "grok-4.6-latest", "grok-4.7", "grok-4.7-latest",
		"grok-4.3", "grok-4.3-latest", "grok-3-mini", "grok-3-mini-fast",
		"grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20-multi-agent-0309":
		supported = true
	}
	normalize := func(raw any) (string, bool) {
		value := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(jsonStringValue(raw))))
		switch value {
		case "none", "low", "medium", "high":
			return value, supported
		case "minimal":
			return "low", supported
		case "xhigh", "extrahigh":
			if strings.HasPrefix(lower, "grok-4.6") || strings.HasPrefix(lower, "grok-4.7") {
				return "xhigh", supported
			}
			return "high", supported
		case "max", "ultra":
			return "high", supported
		}
		return "", false
	}

	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if raw, exists := reasoning["effort"]; exists {
			if effort, keep := normalize(raw); keep {
				reasoning["effort"] = effort
			} else {
				delete(reasoning, "effort")
			}
		}
		if len(reasoning) == 0 {
			delete(payload, "reasoning")
		}
	}
	if raw, exists := payload["reasoning_effort"]; exists {
		if effort, keep := normalize(raw); keep {
			payload["reasoning_effort"] = effort
		} else {
			delete(payload, "reasoning_effort")
		}
	}
}

func deleteFieldRecursive(value any, field string) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, field)
		for _, child := range typed {
			deleteFieldRecursive(child, field)
		}
	case []any:
		for _, child := range typed {
			deleteFieldRecursive(child, field)
		}
	}
}

// stripRedundantViewImageTool removes Codex's local view_image tool when the
// current user turn already carries an inline image: Grok otherwise tends to
// announce a view_image call without making it.
func stripRedundantViewImageTool(payload map[string]any) {
	items, _ := payload["input"].([]any)
	if len(items) == 0 {
		return
	}
	current, ok := items[len(items)-1].(map[string]any)
	if !ok || jsonStringValue(current["role"]) != "user" || !containsInputImage(current["content"]) {
		return
	}
	if choice, ok := payload["tool_choice"].(map[string]any); ok && jsonStringValue(choice["type"]) == "function" {
		name := jsonStringValue(choice["name"])
		if function, ok := choice["function"].(map[string]any); ok && name == "" {
			name = jsonStringValue(function["name"])
		}
		if name == "view_image" {
			return
		}
	}
	tools, _ := payload["tools"].([]any)
	filtered := make([]any, 0, len(tools))
	for _, raw := range tools {
		if tool, ok := raw.(map[string]any); ok && jsonStringValue(tool["type"]) == "function" && jsonStringValue(tool["name"]) == "view_image" {
			continue
		}
		filtered = append(filtered, raw)
	}
	if len(filtered) == len(tools) {
		return
	}
	if len(filtered) == 0 {
		if jsonStringValue(payload["tool_choice"]) == "required" {
			return
		}
		delete(payload, "tools")
		delete(payload, "parallel_tool_calls")
		return
	}
	payload["tools"] = filtered
}

func containsInputImage(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if containsInputImage(item) {
				return true
			}
		}
	case map[string]any:
		switch jsonLowerString(typed["type"]) {
		case "input_image", "image_url", "image":
			return true
		}
		for _, child := range typed {
			if containsInputImage(child) {
				return true
			}
		}
	}
	return false
}

// stripInputNulls drops explicit JSON null object members from input items;
// xAI's untagged ModelInput decoder rejects them with 422. Compaction items
// are left untouched per the compact contract.
func stripInputNulls(value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			stripInputNulls(item)
		}
	case map[string]any:
		if isOpenAICompactionType(jsonLowerString(typed["type"])) {
			return
		}
		for key, child := range typed {
			if child == nil {
				delete(typed, key)
				continue
			}
			stripInputNulls(child)
		}
	}
}

var supportedToolTypes = map[string]bool{
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

// sanitizeTools filters tool declarations to the types the gateway supports,
// repairs function schemas it rejects, and drops tool controls that would
// point at a removed tool.
func sanitizeTools(payload map[string]any) {
	rawTools, exists := payload["tools"]
	tools, isArray := rawTools.([]any)
	if !exists || !isArray || len(tools) == 0 {
		delete(payload, "tools")
		delete(payload, "tool_choice")
		delete(payload, "parallel_tool_calls")
		return
	}

	kept := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.TrimSpace(jsonStringValue(tool["type"]))
		if !supportedToolTypes[toolType] {
			continue
		}
		if toolType == "function" {
			if parameters, has := tool["parameters"]; !has || parameters == nil {
				tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			} else if hasInvalidUnionRoot(parameters) {
				tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
				if strict, _ := tool["strict"].(bool); strict {
					tool["strict"] = false
				}
			}
		}
		kept = append(kept, tool)
	}
	// tool_search was lowered to a function proxy, so defer_loading no longer
	// has a loader on the upstream side.
	for _, raw := range kept {
		delete(raw.(map[string]any), "defer_loading")
	}
	if len(kept) == 0 {
		delete(payload, "tools")
		delete(payload, "tool_choice")
		delete(payload, "parallel_tool_calls")
		return
	}
	payload["tools"] = kept

	choice, ok := payload["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	choiceType := strings.TrimSpace(jsonStringValue(choice["type"]))
	if choiceType == "" {
		return
	}
	if !supportedToolTypes[choiceType] {
		delete(payload, "tool_choice")
		return
	}
	if choiceType != "function" {
		return
	}
	name := strings.TrimSpace(jsonStringValue(choice["name"]))
	if function, ok := choice["function"].(map[string]any); ok && name == "" {
		name = strings.TrimSpace(jsonStringValue(function["name"]))
	}
	if name == "" {
		return
	}
	for _, raw := range kept {
		tool := raw.(map[string]any)
		toolName := strings.TrimSpace(jsonStringValue(tool["name"]))
		if function, ok := tool["function"].(map[string]any); ok && toolName == "" {
			toolName = strings.TrimSpace(jsonStringValue(function["name"]))
		}
		if jsonStringValue(tool["type"]) == "function" && toolName == name {
			return
		}
	}
	delete(payload, "tool_choice")
}

// hasInvalidUnionRoot reports an anyOf/oneOf parameter root with a non-object
// branch, which the gateway's function schema validator rejects.
func hasInvalidUnionRoot(parameters any) bool {
	schema, ok := parameters.(map[string]any)
	if !ok {
		return false
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, _ := schema[keyword].([]any)
		for _, rawBranch := range branches {
			branch, _ := rawBranch.(map[string]any)
			if !strings.EqualFold(strings.TrimSpace(jsonStringValue(branch["type"])), "object") {
				return true
			}
		}
	}
	return false
}

// explicitSessionHeaders are stable conversation identifiers sent by
// OpenAI-compatible clients (Codex, OpenCode, CodeBuddy). Per-request IDs
// would defeat prompt caching and must not be listed here.
var explicitSessionHeaders = []string{
	"session-id",
	"session_id",
	"conversation_id",
	"x-session-affinity",
	"x-session-id",
	"x-opencode-session",
	"x-conversation-id",
}

var claudeCodeSessionSuffix = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// claudeCodeSessionID reads the Claude Code conversation id from its header
// or from metadata.user_id, which is stripped before the request leaves.
func claudeCodeSessionID(c *gin.Context, payload map[string]any) string {
	if session := strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")); session != "" {
		return session
	}
	metadata, _ := payload["metadata"].(map[string]any)
	userID := strings.TrimSpace(jsonStringValue(metadata["user_id"]))
	if matches := claudeCodeSessionSuffix.FindStringSubmatch(userID); len(matches) == 2 {
		return matches[1]
	}
	if strings.HasPrefix(userID, "{") {
		return strings.TrimSpace(gjson.Get(userID, "session_id").String())
	}
	return ""
}

// grokCacheIdentity derives a stable, tenant-isolated routing identity for
// xAI's server-side prompt cache, sent as prompt_cache_key and X-Grok-Conv-Id.
// Seeds are tried from the most to the least specific conversation signal;
// the identity is a hash, so client session identifiers never reach xAI, and
// tenants sharing one subscription cannot collide. Empty when no token or
// seed is available.
func grokCacheIdentity(c *gin.Context, payload map[string]any, claudeSession string, tokenID int, model string) string {
	if tokenID <= 0 || model == "" {
		return ""
	}
	seed := claudeSession
	for _, header := range explicitSessionHeaders {
		if seed != "" {
			break
		}
		seed = strings.TrimSpace(c.GetHeader(header))
	}
	if seed == "" {
		// grok-build side-calls point prompt_cache_key at the parent session
		// while X-Grok-Conv-Id carries a fresh per-call label, so the body
		// field wins.
		seed = strings.TrimSpace(jsonStringValue(payload["prompt_cache_key"]))
	}
	if seed == "" {
		seed = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if previousID := strings.TrimSpace(jsonStringValue(payload["previous_response_id"])); seed == "" && strings.HasPrefix(previousID, "resp_") {
		seed = "grok-prev-resp:" + previousID
	}
	if seed == "" {
		seed = stablePrefixSeed(payload)
	}
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "grok-prompt-cache:v1:%d:%s:%s", tokenID, strings.ToLower(model), seed))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// stablePrefixSeed fingerprints the reusable request prefix (tools,
// instructions, system/developer messages), falling back to the first user
// message so unrelated prompts do not share one tenant-wide identity.
func stablePrefixSeed(payload map[string]any) string {
	var seed strings.Builder
	appendPart := func(label string, value any) {
		encoded, err := common.Marshal(value)
		if err != nil {
			return
		}
		switch string(encoded) {
		case "", `""`, "[]", "{}", "null":
			return
		}
		seed.WriteString("|" + label + "=")
		seed.Write(encoded)
	}
	appendPart("tools", payload["tools"])
	appendPart("instructions", payload["instructions"])
	items, _ := payload["input"].([]any)
	for _, rawItem := range items {
		if item, ok := rawItem.(map[string]any); ok {
			if role := jsonStringValue(item["role"]); role == "system" || role == "developer" {
				appendPart(role, item["content"])
			}
		}
	}
	if seed.Len() > 0 {
		return "prefix" + seed.String()
	}
	if text, ok := payload["input"].(string); ok && strings.TrimSpace(text) != "" {
		return "anchor|" + text
	}
	for _, rawItem := range items {
		if item, ok := rawItem.(map[string]any); ok && jsonStringValue(item["role"]) == "user" {
			appendPart("user", item["content"])
			if seed.Len() > 0 {
				return "anchor" + seed.String()
			}
			return ""
		}
	}
	return ""
}
