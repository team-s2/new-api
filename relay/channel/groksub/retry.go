package groksub

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

// xAI fails long sessions with 400/422 when replayed encrypted reasoning or
// compaction blobs cannot be decrypted (e.g. issued for another decoder or
// cache context). The opaque replay state is dropped and the converted request
// retried once; the model continues from the visible history. Ported from
// sub2api's forwardGrokResponses replay-decode retry.

// grokErrorMessages collects the message fields xAI has used across its flat
// and nested error envelopes.
func grokErrorMessages(body []byte) []string {
	messages := make([]string, 0, 4)
	for _, path := range []string{"error.message", "error.error", "message", "detail"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			messages = append(messages, value)
		}
	}
	if node := gjson.GetBytes(body, "error"); node.Type == gjson.String {
		if value := strings.TrimSpace(node.String()); value != "" {
			messages = append(messages, value)
		}
	}
	if !gjson.ValidBytes(body) {
		if plaintext := strings.TrimSpace(string(body)); plaintext != "" {
			messages = append(messages, plaintext)
		}
	}
	return messages
}

// isEncryptedContentFailure matches an undecryptable encrypted_content or
// compaction blob, e.g.
// {"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}.
func isEncryptedContentFailure(body []byte) bool {
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	if errorNode := gjson.GetBytes(body, "error"); code == "" && errorNode.IsObject() {
		code = strings.TrimSpace(errorNode.Get("code").String())
	}
	switch strings.ToLower(code) {
	case "invalid_encrypted_content", "invalid_compaction", "compaction_decode_error":
		return true
	case "invalid-argument", "":
	default:
		// Keep the official flat-code gate so unrelated 400s are not retried.
		return false
	}
	for _, candidate := range grokErrorMessages(body) {
		message := strings.ToLower(candidate)
		// Nested envelopes may omit the code; require decrypt/decode text then.
		if code == "" && !strings.Contains(message, "decrypt") && !strings.Contains(message, "decode the compaction blob") {
			continue
		}
		if strings.Contains(message, "encrypted_content") && (strings.Contains(message, "decrypt") || strings.Contains(message, "unmodified")) {
			return true
		}
		if strings.Contains(message, "decode the compaction blob") {
			return true
		}
	}
	return false
}

// isCompactionReplayDecodeFailure matches broader decode+replay signals.
func isCompactionReplayDecodeFailure(body []byte) bool {
	for _, candidate := range grokErrorMessages(body) {
		message := strings.ToLower(candidate)
		decodeSignal := strings.Contains(message, "decode") || strings.Contains(message, "deserialize") || strings.Contains(message, "decoder")
		replaySignal := strings.Contains(message, "compaction") || strings.Contains(message, "summary") ||
			strings.Contains(message, "encrypted_content") || strings.Contains(message, "response history")
		if decodeSignal && replaySignal {
			return true
		}
	}
	return false
}

// replayRetryBody derives the single retry body for a replay-decode failure
// of the converted upstream request. ok is false when the failure is not a
// replay failure or nothing can be stripped.
func replayRetryBody(requestBody, errorBody []byte) ([]byte, bool) {
	encryptedFailure := isEncryptedContentFailure(errorBody)
	if !encryptedFailure && !isCompactionReplayDecodeFailure(errorBody) {
		return nil, false
	}
	var payload map[string]any
	if err := common.Unmarshal(requestBody, &payload); err != nil {
		return nil, false
	}
	changed := false
	if !encryptedFailure {
		// Keep compaction summaries as visible context before trimming blobs.
		changed = convertCompactInputs(payload)
	}
	changed = trimEncryptedReplayItems(payload) || changed
	if !encryptedFailure {
		changed = dropEmptyReplayReasoning(payload) || changed
		if previousID, _ := payload["previous_response_id"].(string); strings.TrimSpace(previousID) != "" && !hasFunctionCallOutput(payload) {
			delete(payload, "previous_response_id")
			if _, exists := payload["store"]; !exists {
				payload["store"] = false
			}
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// trimEncryptedReplayItems drops encrypted compaction items and strips
// encrypted_content from reasoning items, dropping those left empty.
func trimEncryptedReplayItems(payload map[string]any) bool {
	items, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	filtered := make([]any, 0, len(items))
	changed := false
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			filtered = append(filtered, rawItem)
			continue
		}
		_, encrypted := item["encrypted_content"]
		switch strings.TrimSpace(jsonStringValue(item["type"])) {
		case "compaction", "compaction_summary":
			if encrypted {
				changed = true
				continue
			}
		case "reasoning":
			itemChanged := encrypted
			delete(item, "encrypted_content")
			if content, has := item["content"]; has && content == nil {
				delete(item, "content")
				itemChanged = true
			}
			if itemChanged {
				changed = true
				if len(item) == 1 {
					continue
				}
			}
		}
		filtered = append(filtered, item)
	}
	if !changed {
		return false
	}
	if len(filtered) == 0 {
		delete(payload, "input")
	} else {
		payload["input"] = filtered
	}
	return true
}

// dropEmptyReplayReasoning removes reasoning items with nothing left to replay.
func dropEmptyReplayReasoning(payload map[string]any) bool {
	items, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	filtered := make([]any, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(jsonStringValue(item["type"])) != "reasoning" {
			filtered = append(filtered, rawItem)
			continue
		}
		summary, _ := item["summary"].([]any)
		content, hasContent := item["content"]
		_, hasEncrypted := item["encrypted_content"]
		if hasEncrypted || len(summary) > 0 || (hasContent && content != nil) {
			filtered = append(filtered, rawItem)
		}
	}
	if len(filtered) == len(items) {
		return false
	}
	payload["input"] = filtered
	return true
}

func hasFunctionCallOutput(payload map[string]any) bool {
	items, _ := payload["input"].([]any)
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if jsonLowerString(item["type"]) == "function_call_output" {
			return true
		}
		if role := jsonLowerString(item["role"]); role == "tool" || role == "function" {
			return true
		}
	}
	return false
}
