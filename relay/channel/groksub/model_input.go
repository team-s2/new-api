package groksub

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// sanitizeModelInput converts replay items from OpenAI and chat shapes into
// the subset accepted by xAI's ModelInput decoder. Call IDs are assigned in a
// separate pass so an output can safely appear before its call. String input
// is returned unchanged; a single object is treated as a one-item array.
// Ported from sub2api's sanitizeGrokResponsesModelInput.

type toolOutputImage struct {
	callID string
	url    string
}

func sanitizeModelInput(input any) any {
	var items []any
	switch typed := input.(type) {
	case []any:
		items = typed
	case map[string]any:
		items = []any{typed}
	default:
		return input
	}

	callIDs, outputIDs := pairReplayCallIDs(items)
	filtered := make([]any, 0, len(items))
	pendingImages := make([]toolOutputImage, 0)
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			filtered = appendToolOutputImages(filtered, pendingImages)
			pendingImages = pendingImages[:0]
			if text, isText := rawItem.(string); isText && strings.TrimSpace(text) != "" {
				filtered = append(filtered, map[string]any{"type": "message", "role": "user", "content": text})
			}
			continue
		}

		itemType := jsonLowerString(item["type"])
		role := jsonLowerString(item["role"])
		if role == "tool" || role == "function" || isReplayOutputType(itemType) {
			callID := outputIDs[index]
			rawOutput := firstNonNil(item["output"], item["content"], item["results"])
			output, nestedImages := normalizeToolOutput(rawOutput, callID)
			filtered = append(filtered, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  output,
			})
			pendingImages = append(pendingImages, extractTopLevelToolImages(item, callID)...)
			pendingImages = append(pendingImages, nestedImages...)
			continue
		}
		filtered = appendToolOutputImages(filtered, pendingImages)
		pendingImages = pendingImages[:0]

		switch itemType {
		case "text", "input_text", "output_text":
			text := strings.TrimSpace(jsonStringValue(item["text"]))
			if text == "" {
				continue
			}
			if role == "" {
				role = "user"
			}
			item = map[string]any{"type": "message", "role": role, "content": text}
			itemType = "message"
		case "custom_tool_call", "tool_search_call":
			originalType := itemType
			item["type"] = "function_call"
			itemType = "function_call"
			if strings.TrimSpace(jsonStringValue(item["name"])) == "" && originalType == "tool_search_call" {
				item["name"] = "tool_search"
			}
		}

		if itemType == "" && role != "" {
			itemType = "message"
			item["type"] = itemType
		}
		if itemType == "message" {
			if role == "" {
				item["role"] = "user"
			}
			content, keep := sanitizeMessageContent(item["content"])
			if !keep {
				continue
			}
			item["content"] = content
			if jsonLowerString(item["role"]) == "assistant" && !isCompleteOutputMessage(item) {
				if text, ok := collapseAssistantOutputText(content); ok {
					item["content"] = text
				}
				delete(item, "id")
				delete(item, "status")
			}
		} else if itemType == "reasoning" {
			delete(item, "status")
			if content, exists := item["content"]; exists && content == nil {
				delete(item, "content")
			}
		} else if itemType == "function_call" {
			name := strings.TrimSpace(jsonStringValue(item["name"]))
			arguments := item["arguments"]
			if function, ok := item["function"].(map[string]any); ok {
				if name == "" {
					name = strings.TrimSpace(jsonStringValue(function["name"]))
				}
				if arguments == nil {
					arguments = function["arguments"]
				}
			}
			if arguments == nil {
				arguments = firstNonNil(item["input"], item["query"])
				if _, custom := item["input"]; custom {
					arguments = map[string]any{"input": arguments}
				}
			}
			if name == "" {
				name = "unknown_tool"
			}
			item["call_id"] = callIDs[index]
			item["name"] = name
			item["arguments"] = modelInputString(arguments, "{}")
			for _, field := range []string{"id", "status", "tool_call_id", "function", "input", "query", "execution"} {
				delete(item, field)
			}
		}
		if shouldStripNonPairCallID(itemType) {
			delete(item, "call_id")
		}
		filtered = append(filtered, item)
	}
	return appendToolOutputImages(filtered, pendingImages)
}

// normalizeToolOutput coerces a tool output value into the plain string xAI
// requires, lifting any embedded image parts out as follow-up input images.
func normalizeToolOutput(value any, callID string) (string, []toolOutputImage) {
	stripped, images, keep := stripToolOutputImages(value, callID)
	if len(images) == 0 {
		return modelInputString(value, "(empty)"), nil
	}
	if !keep {
		return "(empty)", images
	}
	return structuredToolOutputString(stripped, "(empty)"), images
}

func stripToolOutputImages(value any, callID string) (any, []toolOutputImage, bool) {
	switch typed := value.(type) {
	case []any:
		filtered := make([]any, 0, len(typed))
		images := make([]toolOutputImage, 0)
		for _, item := range typed {
			stripped, nested, keep := stripToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered = append(filtered, stripped)
			}
		}
		return filtered, images, len(filtered) > 0
	case map[string]any:
		partType := jsonLowerString(typed["type"])
		if partType == "image" || partType == "image_url" || partType == "input_image" {
			url := toolOutputImageURL(typed)
			if url == "" || isEmptyBase64DataURI(url) {
				return nil, nil, false
			}
			return nil, []toolOutputImage{{callID: callID, url: url}}, false
		}

		filtered := make(map[string]any, len(typed))
		for key, item := range typed {
			filtered[key] = item
		}
		images := make([]toolOutputImage, 0)
		if rawImages, exists := filtered["images"]; exists {
			if values, ok := rawImages.([]any); ok {
				for _, rawImage := range values {
					if url := toolOutputImageURL(rawImage); url != "" && !isEmptyBase64DataURI(url) {
						images = append(images, toolOutputImage{callID: callID, url: url})
					}
				}
				delete(filtered, "images")
			}
		}
		for _, field := range []string{"content", "output", "results"} {
			item, exists := filtered[field]
			if !exists {
				continue
			}
			stripped, nested, keep := stripToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered[field] = stripped
			} else {
				delete(filtered, field)
			}
		}
		return filtered, images, len(filtered) > 0
	default:
		return value, nil, value != nil
	}
}

// structuredToolOutputString flattens pure-text structured output parts into
// one string; anything richer falls back to JSON encoding.
func structuredToolOutputString(value any, fallback string) string {
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return modelInputString(value, fallback)
	}

	texts := make([]string, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return modelInputString(value, fallback)
		}
		partType := jsonLowerString(part["type"])
		if partType != "text" && partType != "input_text" && partType != "output_text" {
			return modelInputString(value, fallback)
		}
		if text := strings.TrimSpace(jsonStringValue(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return fallback
	}
	return strings.Join(texts, "\n")
}

// extractTopLevelToolImages lifts Grok-Shell style output images attached as
// function_call_output.images.
func extractTopLevelToolImages(item map[string]any, callID string) []toolOutputImage {
	rawImages, ok := item["images"].([]any)
	if !ok || len(rawImages) == 0 {
		return nil
	}
	images := make([]toolOutputImage, 0, len(rawImages))
	for _, rawImage := range rawImages {
		if url := toolOutputImageURL(rawImage); url != "" && !isEmptyBase64DataURI(url) {
			images = append(images, toolOutputImage{callID: callID, url: url})
		}
	}
	return images
}

func toolOutputImageURL(value any) string {
	switch image := value.(type) {
	case string:
		return strings.TrimSpace(image)
	case map[string]any:
		for _, field := range []string{"url", "image_url", "file_url"} {
			switch typed := image[field].(type) {
			case string:
				if url := strings.TrimSpace(typed); url != "" {
					return url
				}
			case map[string]any:
				if url := strings.TrimSpace(jsonStringValue(typed["url"])); url != "" {
					return url
				}
			}
		}
	}
	return ""
}

func appendToolOutputImages(items []any, images []toolOutputImage) []any {
	if len(images) == 0 {
		return items
	}

	content := make([]any, 0, len(images)*2)
	lastCallID := ""
	for _, image := range images {
		if image.callID != lastCallID {
			content = append(content, map[string]any{
				"type": "input_text",
				"text": "[Tool output media for call " + image.callID + "]",
			})
			lastCallID = image.callID
		}
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": image.url,
		})
	}
	return append(items, map[string]any{
		"type":    "message",
		"role":    "user",
		"content": content,
	})
}

// pairReplayCallIDs assigns a canonical call_id to every call/output pair in
// two passes so outputs may precede their calls in the replay history.
func pairReplayCallIDs(items []any) (map[int]string, map[int]string) {
	callIDs := make(map[int]string)
	outputIDs := make(map[int]string)
	aliases := make(map[string]string)
	conflicting := make(map[string]struct{})
	pendingCalls := make([]string, 0)
	nextID := 0
	synthetic := func() string {
		nextID++
		return "grok_replay_call_" + strconv.Itoa(nextID)
	}
	registerAlias := func(alias, canonical string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		if _, conflict := conflicting[alias]; conflict {
			return
		}
		if existing, exists := aliases[alias]; exists && existing != canonical {
			delete(aliases, alias)
			conflicting[alias] = struct{}{}
			return
		}
		aliases[alias] = canonical
	}

	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := jsonLowerString(item["type"])
		if !isReplayCallType(itemType) {
			continue
		}
		callID := firstNonEmptyString(jsonStringValue(item["call_id"]), jsonStringValue(item["tool_call_id"]))
		if callID == "" {
			callID = synthetic()
		}
		callIDs[index] = callID
		pendingCalls = append(pendingCalls, callID)
		registerAlias(callID, callID)
		registerAlias(jsonStringValue(item["call_id"]), callID)
		registerAlias(jsonStringValue(item["tool_call_id"]), callID)
		registerAlias(jsonStringValue(item["id"]), callID)
	}

	consumed := make(map[string]struct{})
	consumeNext := func() string {
		for _, callID := range pendingCalls {
			if _, used := consumed[callID]; used {
				continue
			}
			consumed[callID] = struct{}{}
			return callID
		}
		return synthetic()
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := jsonLowerString(item["type"])
		role := jsonLowerString(item["role"])
		if role != "tool" && role != "function" && !isReplayOutputType(itemType) {
			continue
		}
		alias := firstNonEmptyString(jsonStringValue(item["call_id"]), jsonStringValue(item["tool_call_id"]), jsonStringValue(item["id"]))
		_, conflict := conflicting[alias]
		if canonical := aliases[alias]; canonical != "" && !conflict {
			outputIDs[index] = canonical
			consumed[canonical] = struct{}{}
		} else if firstNonEmptyString(jsonStringValue(item["call_id"]), jsonStringValue(item["tool_call_id"])) != "" {
			// Explicit call identifiers remain authoritative even when an
			// invalid transcript reuses one identifier for multiple calls.
			outputIDs[index] = alias
		} else if conflict {
			outputIDs[index] = synthetic()
		} else {
			outputIDs[index] = consumeNext()
		}
	}
	return callIDs, outputIDs
}

func isReplayCallType(itemType string) bool {
	switch itemType {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	}
	return false
}

func isReplayOutputType(itemType string) bool {
	switch itemType {
	case "function_call_output", "custom_tool_call_output", "tool_search_output", "tool_search_call_output":
		return true
	}
	return false
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sanitizeMessageContent(content any) (any, bool) {
	switch value := content.(type) {
	case nil:
		return nil, false
	case string:
		return value, strings.TrimSpace(value) != ""
	case []any:
		filtered := make([]any, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				if text, isText := rawPart.(string); !isText || strings.TrimSpace(text) != "" {
					filtered = append(filtered, rawPart)
				}
				continue
			}
			switch jsonLowerString(part["type"]) {
			case "text", "input_text", "output_text":
				if strings.TrimSpace(jsonStringValue(part["text"])) == "" {
					continue
				}
			case "image_url", "input_image":
				if !contentPartHasImageURL(part) {
					continue
				}
			}
			filtered = append(filtered, part)
		}
		return filtered, len(filtered) > 0
	default:
		return content, true
	}
}

func contentPartHasImageURL(part map[string]any) bool {
	for _, field := range []string{"file_id", "file_data"} {
		if strings.TrimSpace(jsonStringValue(part[field])) != "" {
			return true
		}
	}
	raw := firstNonNil(part["image_url"], part["file_url"])
	url := ""
	switch value := raw.(type) {
	case string:
		url = strings.TrimSpace(value)
	case map[string]any:
		url = strings.TrimSpace(jsonStringValue(value["url"]))
	}
	return url != "" && !isEmptyBase64DataURI(url)
}

// isEmptyBase64DataURI reports a data: URI whose base64 payload is empty.
func isEmptyBase64DataURI(raw string) bool {
	rest, ok := strings.CutPrefix(raw, "data:")
	if !ok {
		return false
	}
	_, payload, ok := strings.Cut(rest, ";")
	if !ok {
		return false
	}
	encoded, ok := strings.CutPrefix(payload, "base64,")
	return ok && strings.TrimSpace(encoded) == ""
}

func jsonStringValue(value any) string {
	text, _ := value.(string)
	return text
}

// jsonLowerString reads a string field and lowercases/trims it for type
// comparisons.
func jsonLowerString(value any) string {
	return strings.ToLower(strings.TrimSpace(jsonStringValue(value)))
}

// modelInputString coerces a value to the string xAI's decoder requires:
// strings pass through, anything else is JSON-encoded.
func modelInputString(value any, fallback string) string {
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return fallback
		}
		return text
	}
	if value == nil {
		return fallback
	}
	encoded, err := common.Marshal(value)
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return fallback
	}
	return string(encoded)
}

func isCompleteOutputMessage(item map[string]any) bool {
	return strings.TrimSpace(jsonStringValue(item["id"])) != "" &&
		strings.TrimSpace(jsonStringValue(item["status"])) != ""
}

// collapseAssistantOutputText flattens pure output_text assistant content
// into a plain string for incomplete replay messages.
func collapseAssistantOutputText(content any) (string, bool) {
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return "", false
	}
	var text strings.Builder
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok || jsonLowerString(part["type"]) != "output_text" {
			return "", false
		}
		value, ok := part["text"].(string)
		if !ok {
			return "", false
		}
		text.WriteString(value)
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", false
	}
	return text.String(), true
}

// shouldStripNonPairCallID drops call_id from item types that xAI accepts
// without pairing semantics.
func shouldStripNonPairCallID(itemType string) bool {
	switch itemType {
	case "message", "reasoning":
		return true
	}
	return false
}
