package groksub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The Grok gateway only understands function tools. Codex-style clients send
// custom tools (free-text input), tool_search, namespace tool groups, and the
// call items they produce. These are reversibly lowered to function tools on
// the way out and restored on the way back. Ported from sub2api's apicompat
// Responses client-tool adapter.

const (
	toolSearchProxyName     = "tool_search"
	namespaceToolNameMaxLen = 64

	customToolInputSchema = `{"type":"object","properties":{"input":{"type":"string","description":"The raw input for this tool, passed through verbatim."}},"required":["input"]}`
	toolSearchProxySchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for tools or connectors to load."},"limit":{"type":"integer","description":"Maximum number of tool groups to return."}},"required":["query"]}`
	toolSearchProxyDesc   = "Search and load Codex tools, plugins, connectors, and MCP namespaces for the current task."
)

type namespacedToolName struct {
	Namespace string
	Name      string
}

// clientToolMapping records the reversible lowering applied before a request
// is sent so responses can be restored.
type clientToolMapping struct {
	customTools    map[string]bool
	toolSearch     bool
	namespaceTools map[string]namespacedToolName
}

func (m *clientToolMapping) empty() bool {
	return m == nil || (len(m.customTools) == 0 && !m.toolSearch && len(m.namespaceTools) == 0)
}

func (m *clientToolMapping) kindOf(name string) string {
	switch {
	case m.customTools[name]:
		return "custom"
	case m.toolSearch && name == toolSearchProxyName:
		return "search"
	}
	return ""
}

// adaptClientTools lowers client-only tool declarations, replay history and
// tool_choice in req to the function protocol. It mutates req and returns the
// mapping required to restore the upstream response (nil when nothing was
// lowered).
func adaptClientTools(req map[string]any) (*clientToolMapping, error) {
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, nil
	}
	if err := promoteToolSearchDiscoveries(req); err != nil {
		return nil, err
	}
	tools, _ = req["tools"].([]any)

	mapping := &clientToolMapping{customTools: make(map[string]bool)}
	functionNames := make(map[string]bool)
	customNames := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(jsonStringValue(tool["name"]))
		switch strings.TrimSpace(jsonStringValue(tool["type"])) {
		case "function":
			if name != "" {
				functionNames[name] = true
			}
		case "custom":
			if name != "" {
				customNames[name] = true
			}
		case "tool_search":
			mapping.toolSearch = true
		}
	}
	for name := range customNames {
		if functionNames[name] {
			return nil, fmt.Errorf("custom tool %q conflicts with a function tool of the same name; this upstream cannot disambiguate them, rename one of the tools", name)
		}
	}
	if mapping.toolSearch && (functionNames[toolSearchProxyName] || customNames[toolSearchProxyName]) {
		return nil, fmt.Errorf("built-in tool_search conflicts with a declared tool named %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
	}

	names, err := flattenNamespaceTools(req)
	if err != nil {
		return nil, err
	}
	if _, exists := names[toolSearchProxyName]; exists && mapping.toolSearch {
		return nil, fmt.Errorf("built-in tool_search conflicts with namespace tool flattened as %q; this upstream cannot disambiguate them, rename the tool", toolSearchProxyName)
	}
	mapping.namespaceTools = names

	tools, _ = req["tools"].([]any)
	lowered := make([]any, 0, len(tools))
	seenSearch := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			lowered = append(lowered, raw)
			continue
		}
		name := strings.TrimSpace(jsonStringValue(tool["name"]))
		switch strings.TrimSpace(jsonStringValue(tool["type"])) {
		case "custom":
			if name == "" {
				lowered = append(lowered, raw)
				continue
			}
			copied := copyJSONObject(tool)
			copied["type"] = "function"
			copied["parameters"] = json.RawMessage(customToolInputSchema)
			delete(copied, "format")
			mapping.customTools[name] = true
			lowered = append(lowered, copied)
		case "tool_search":
			if seenSearch {
				continue
			}
			seenSearch = true
			lowered = append(lowered, map[string]any{
				"type":        "function",
				"name":        toolSearchProxyName,
				"description": toolSearchProxyDesc,
				"parameters":  json.RawMessage(toolSearchProxySchema),
			})
		default:
			lowered = append(lowered, raw)
		}
	}
	req["tools"] = lowered

	if err := rewriteClientToolHistory(req["input"], mapping); err != nil {
		return nil, err
	}
	if choice, ok := req["tool_choice"].(map[string]any); ok {
		name := strings.TrimSpace(jsonStringValue(choice["name"]))
		switch strings.TrimSpace(jsonStringValue(choice["type"])) {
		case "custom":
			if mapping.customTools[name] {
				choice["type"] = "function"
			}
		case "tool_search":
			if mapping.toolSearch {
				req["tool_choice"] = map[string]any{"type": "function", "name": toolSearchProxyName}
			}
		}
	}
	if mapping.empty() {
		return nil, nil
	}
	return mapping, nil
}

func copyJSONObject(source map[string]any) map[string]any {
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}

// flattenNamespaceTools converts Codex namespace declarations into top-level
// "<namespace>__<name>" function tools and rewrites namespace-qualified calls.
func flattenNamespaceTools(req map[string]any) (map[string]namespacedToolName, error) {
	tools, _ := req["tools"].([]any)
	topLevel := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		toolType := strings.TrimSpace(jsonStringValue(tool["type"]))
		if name := strings.TrimSpace(jsonStringValue(tool["name"])); (toolType == "function" || toolType == "custom") && name != "" {
			topLevel[name] = true
		}
	}

	names := make(map[string]namespacedToolName)
	flattened := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(jsonStringValue(tool["type"])) != "namespace" {
			flattened = append(flattened, raw)
			continue
		}
		namespace := strings.TrimSpace(jsonStringValue(tool["name"]))
		if namespace == "" {
			flattened = append(flattened, raw)
			continue
		}
		for _, rawChild := range namespaceChildren(tool) {
			child, ok := rawChild.(map[string]any)
			if !ok || strings.TrimSpace(jsonStringValue(child["type"])) != "function" {
				continue
			}
			name := strings.TrimSpace(jsonStringValue(child["name"]))
			if name == "" {
				continue
			}
			flat := flattenNamespaceToolName(namespace, name)
			entry := namespacedToolName{Namespace: namespace, Name: name}
			if topLevel[flat] {
				return nil, fmt.Errorf("namespace tool %q/%q flattens to %q which conflicts with a top-level tool of the same name; this upstream cannot disambiguate them, rename one of the tools", namespace, name, flat)
			}
			if previous, exists := names[flat]; exists {
				if previous != entry {
					return nil, fmt.Errorf("namespace tools %q/%q and %q/%q both flatten to %q; this upstream cannot disambiguate them, rename one of the tools", previous.Namespace, previous.Name, namespace, name, flat)
				}
				continue
			}
			names[flat] = entry
			flatChild := copyJSONObject(child)
			flatChild["name"] = flat
			flattened = append(flattened, flatChild)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	req["tools"] = flattened
	rewriteNamespaceQualifiedCalls(req["input"], names)
	if choice, ok := req["tool_choice"].(map[string]any); ok {
		if strings.TrimSpace(jsonStringValue(choice["type"])) == "namespace" {
			req["tool_choice"] = "auto"
		} else {
			rewriteNamespaceQualifiedCall(choice, names)
		}
	}
	return names, nil
}

func namespaceChildren(tool map[string]any) []any {
	if children, ok := tool["tools"].([]any); ok && len(children) > 0 {
		return children
	}
	children, _ := tool["children"].([]any)
	return children
}

// flattenNamespaceToolName joins namespace and name, hashing the tail when the
// result would exceed the function-name length limit.
func flattenNamespaceToolName(namespace, name string) string {
	full := namespace + "__" + name
	if len(full) <= namespaceToolNameMaxLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	var prefix strings.Builder
	for _, ch := range full {
		if prefix.Len()+len(string(ch)) > namespaceToolNameMaxLen-len(suffix) {
			break
		}
		prefix.WriteRune(ch)
	}
	return prefix.String() + suffix
}

func rewriteNamespaceQualifiedCalls(value any, names map[string]namespacedToolName) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			rewriteNamespaceQualifiedCalls(item, names)
		}
	case map[string]any:
		if strings.TrimSpace(jsonStringValue(typed["type"])) == "function_call" {
			rewriteNamespaceQualifiedCall(typed, names)
		}
		for _, child := range typed {
			rewriteNamespaceQualifiedCalls(child, names)
		}
	}
}

func rewriteNamespaceQualifiedCall(item map[string]any, names map[string]namespacedToolName) {
	namespace := strings.TrimSpace(jsonStringValue(item["namespace"]))
	name := strings.TrimSpace(jsonStringValue(item["name"]))
	if namespace == "" || name == "" {
		return
	}
	flat := flattenNamespaceToolName(namespace, name)
	if names[flat] != (namespacedToolName{Namespace: namespace, Name: name}) {
		return
	}
	item["name"] = flat
	delete(item, "namespace")
}

// promoteToolSearchDiscoveries declares tools a completed client tool_search
// discovered, so a function-only upstream can call them. Discoveries are
// appended after the static tools to keep the declaration prefix stable.
func promoteToolSearchDiscoveries(req map[string]any) error {
	tools, _ := req["tools"].([]any)
	input, _ := req["input"].([]any)
	if len(input) == 0 || !hasToolSearchDeclaration(tools) {
		return nil
	}

	known := make(map[string]discoveredToolIdentity)
	for _, raw := range tools {
		for _, candidate := range discoveredToolCandidates(raw) {
			if previous, exists := known[candidate.key]; exists && previous != candidate.identity {
				previous.ambiguous = true
				known[candidate.key] = previous
				continue
			}
			known[candidate.key] = candidate.identity
		}
	}

	promoted := make([]any, 0)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(jsonStringValue(item["type"])) != "tool_search_output" {
			continue
		}
		if status, present := item["status"]; present && strings.TrimSpace(jsonStringValue(status)) != "completed" {
			continue
		}
		discoveries, _ := item["tools"].([]any)
		for _, rawDiscovery := range discoveries {
			discovery, ok := rawDiscovery.(map[string]any)
			if !ok {
				continue
			}
			candidates := discoveredToolCandidates(discovery)
			admitted := make([]any, 0, len(candidates))
			for _, candidate := range candidates {
				previous, exists := known[candidate.key]
				if !exists {
					known[candidate.key] = candidate.identity
					admitted = append(admitted, candidate.declaration)
					continue
				}
				if previous.ambiguous || previous != candidate.identity {
					return fmt.Errorf("discovered tool %q conflicts with an existing declaration; this upstream cannot safely disambiguate different names, namespaces, or schemas", candidate.key)
				}
			}
			if len(admitted) == 0 {
				continue
			}
			if strings.TrimSpace(jsonStringValue(discovery["type"])) == "namespace" {
				declaration := copyJSONObject(discovery)
				declaration["tools"] = admitted
				delete(declaration, "children")
				promoted = append(promoted, declaration)
			} else {
				promoted = append(promoted, admitted...)
			}
		}
	}
	if len(promoted) > 0 {
		req["tools"] = append(tools, promoted...)
	}
	return nil
}

func hasToolSearchDeclaration(tools []any) bool {
	for _, raw := range tools {
		if tool, ok := raw.(map[string]any); ok && strings.TrimSpace(jsonStringValue(tool["type"])) == "tool_search" {
			return true
		}
	}
	return false
}

type discoveredToolIdentity struct {
	toolType  string
	name      string
	namespace string
	encoded   string
	ambiguous bool
}

type discoveredToolCandidate struct {
	key         string
	declaration map[string]any
	identity    discoveredToolIdentity
}

// discoveredToolCandidates lists the callable identities a function, custom,
// or namespace declaration contributes, keyed by the name the upstream sees.
func discoveredToolCandidates(raw any) []discoveredToolCandidate {
	tool, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	direct := func(tool map[string]any, toolType string) (discoveredToolCandidate, bool) {
		name := strings.TrimSpace(jsonStringValue(tool["name"]))
		if name == "" {
			return discoveredToolCandidate{}, false
		}
		declaration := copyJSONObject(tool)
		declaration["type"] = toolType
		declaration["name"] = name
		identity := declaration
		if toolType == "custom" {
			identity = copyJSONObject(declaration)
			identity["type"] = "function"
			identity["parameters"] = json.RawMessage(customToolInputSchema)
			delete(identity, "format")
		}
		encoded, err := common.Marshal(identity)
		if err != nil {
			return discoveredToolCandidate{}, false
		}
		return discoveredToolCandidate{
			key:         name,
			declaration: declaration,
			identity:    discoveredToolIdentity{toolType: toolType, name: name, encoded: string(encoded)},
		}, true
	}

	switch toolType := strings.TrimSpace(jsonStringValue(tool["type"])); toolType {
	case "function", "custom":
		if candidate, ok := direct(tool, toolType); ok {
			return []discoveredToolCandidate{candidate}
		}
	case "namespace":
		namespace := strings.TrimSpace(jsonStringValue(tool["name"]))
		if namespace == "" {
			return nil
		}
		candidates := make([]discoveredToolCandidate, 0)
		for _, rawChild := range namespaceChildren(tool) {
			child, ok := rawChild.(map[string]any)
			if !ok || strings.TrimSpace(jsonStringValue(child["type"])) != "function" {
				continue
			}
			candidate, ok := direct(child, "function")
			if !ok {
				continue
			}
			candidate.identity.toolType = "namespace"
			candidate.identity.namespace = namespace
			candidate.key = flattenNamespaceToolName(namespace, candidate.identity.name)
			candidates = append(candidates, candidate)
		}
		return candidates
	}
	return nil
}

// rewriteClientToolHistory lowers client tool call/output items in the replay
// history to the function protocol.
func rewriteClientToolHistory(value any, mapping *clientToolMapping) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := rewriteClientToolHistory(item, mapping); err != nil {
				return err
			}
		}
	case map[string]any:
		switch strings.TrimSpace(jsonStringValue(typed["type"])) {
		case "custom_tool_call":
			if mapping.customTools[strings.TrimSpace(jsonStringValue(typed["name"]))] {
				typed["type"] = "function_call"
				arguments, _ := common.Marshal(map[string]string{"input": jsonStringValue(typed["input"])})
				typed["arguments"] = string(arguments)
				delete(typed, "input")
				normalizeLoweredItemID(typed)
			}
		case "custom_tool_call_output":
			typed["type"] = "function_call_output"
			normalizeLoweredItemID(typed)
			if output, exists := typed["output"]; exists && !isToolOutputContent(output) {
				typed["output"] = modelInputString(output, "")
			}
		case "tool_search_call":
			if mapping.toolSearch {
				typed["type"] = "function_call"
				typed["name"] = toolSearchProxyName
				typed["arguments"] = modelInputString(typed["arguments"], "{}")
				delete(typed, "execution")
				normalizeLoweredItemID(typed)
			}
		case "tool_search_output":
			if mapping.toolSearch {
				if strings.TrimSpace(jsonStringValue(typed["call_id"])) == "" {
					return fmt.Errorf("tool_search_output requires a non-empty string call_id before it can be lowered to function_call_output")
				}
				output, hasOutput := typed["output"]
				if !hasOutput {
					tools, hasTools := typed["tools"]
					if !hasTools {
						return fmt.Errorf("tool_search_output requires output or tools before it can be lowered to function_call_output")
					}
					output = tools
				}
				typed["type"] = "function_call_output"
				typed["output"] = modelInputString(output, "")
				normalizeLoweredItemID(typed)
				delete(typed, "tools")
				delete(typed, "status")
				delete(typed, "execution")
			}
		}
		for _, child := range typed {
			if err := rewriteClientToolHistory(child, mapping); err != nil {
				return err
			}
		}
	}
	return nil
}

func isToolOutputContent(output any) bool {
	parts, ok := output.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return false
		}
		switch jsonStringValue(part["type"]) {
		case "input_text", "input_image", "input_file":
		default:
			return false
		}
	}
	return true
}

// Responses item IDs carry a type prefix (fc_, ctc_, tsc_) that strict
// upstreams validate. Lowered items map their ID back to fc_ or drop it.
var toolCallItemIDPrefixes = []string{"fc_", "ctc_", "tsc_"}

func normalizeLoweredItemID(item map[string]any) {
	id := strings.TrimSpace(jsonStringValue(item["id"]))
	if id == "" || strings.HasPrefix(id, "fc") {
		return
	}
	if retyped := retypeToolCallItemID(id, "function_call"); retyped != id {
		item["id"] = retyped
		return
	}
	delete(item, "id")
}

// retypeToolCallItemID re-prefixes an upstream item ID so it agrees with the
// item type it is restored to, keeping the suffix stable.
func retypeToolCallItemID(id, itemType string) string {
	want := map[string]string{"custom_tool_call": "ctc_", "tool_search_call": "tsc_", "function_call": "fc_"}[itemType]
	if want == "" || id == "" || strings.HasPrefix(id, want) {
		return id
	}
	for _, known := range toolCallItemIDPrefixes {
		if known != want && strings.HasPrefix(id, known) {
			return want + strings.TrimPrefix(id, known)
		}
	}
	return id
}

func retypeItemID(item map[string]any, itemType string) {
	if id := jsonStringValue(item["id"]); id != "" {
		item["id"] = retypeToolCallItemID(id, itemType)
	}
}

// restoreClientToolPayload rewrites a JSON payload (non-stream response body
// or terminal stream event) back to the client's original tool shapes.
func restoreClientToolPayload(payload []byte, mapping *clientToolMapping) ([]byte, bool) {
	if len(payload) == 0 || mapping.empty() || !strings.Contains(string(payload), "function_call") {
		return payload, false
	}
	var value any
	if err := common.Unmarshal(payload, &value); err != nil {
		return payload, false
	}
	if !restoreClientToolValue(value, mapping) {
		return payload, false
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return payload, false
	}
	return encoded, true
}

func restoreClientToolValue(value any, mapping *clientToolMapping) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			changed = restoreClientToolValue(item, mapping) || changed
		}
	case map[string]any:
		if strings.TrimSpace(jsonStringValue(typed["type"])) == "function_call" {
			name := strings.TrimSpace(jsonStringValue(typed["name"]))
			switch mapping.kindOf(name) {
			case "custom":
				typed["type"] = "custom_tool_call"
				retypeItemID(typed, "custom_tool_call")
				typed["input"] = extractCustomToolInput(modelInputString(typed["arguments"], ""))
				delete(typed, "arguments")
				delete(typed, "namespace")
				changed = true
			case "search":
				restoreToolSearchItem(typed, modelInputString(typed["arguments"], ""))
				changed = true
			default:
				if entry, ok := mapping.namespaceTools[name]; ok {
					typed["name"] = entry.Name
					typed["namespace"] = entry.Namespace
					changed = true
				}
			}
		}
		for _, child := range typed {
			changed = restoreClientToolValue(child, mapping) || changed
		}
	}
	return changed
}

// restoreToolSearchItem reshapes a proxy function call into Codex's
// tool_search_call: execution is always "client" and arguments is an object.
func restoreToolSearchItem(item map[string]any, arguments string) {
	item["type"] = "tool_search_call"
	retypeItemID(item, "tool_search_call")
	item["execution"] = "client"
	item["arguments"] = toolSearchArgumentsJSON(arguments)
	delete(item, "name")
	delete(item, "namespace")
}

func toolSearchArgumentsJSON(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if gjson.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	encoded, _ := common.Marshal(arguments)
	return encoded
}

// extractCustomToolInput pulls the verbatim input string out of the proxy
// function arguments.
func extractCustomToolInput(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var parsed map[string]json.RawMessage
	if err := common.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed
	}
	raw, ok := parsed["input"]
	if !ok {
		if len(parsed) == 0 {
			return ""
		}
		return trimmed
	}
	var input string
	if err := common.Unmarshal(raw, &input); err != nil {
		return trimmed
	}
	return input
}

// streamEventRestorer restores client tool lifecycles in one Responses SSE
// stream. Proxy argument deltas are buffered until the call completes, and a
// custom tool completion expands into input delta + done events, so sequence
// numbers are reassigned to stay continuous.
type streamEventRestorer struct {
	mapping  *clientToolMapping
	nextSeq  int64
	seenSeq  bool
	calls    map[string]*streamToolCall
	byOutput map[int64]*streamToolCall
}

type streamToolCall struct {
	kind         string
	name         string
	callID       string
	itemID       string
	clientItemID string
	outputIndex  int64
	arguments    strings.Builder
}

func newStreamEventRestorer(mapping *clientToolMapping) *streamEventRestorer {
	return &streamEventRestorer{mapping: mapping, calls: make(map[string]*streamToolCall), byOutput: make(map[int64]*streamToolCall)}
}

// restore transforms one upstream data payload into zero or more payloads.
func (r *streamEventRestorer) restore(payload []byte) [][]byte {
	if !gjson.ValidBytes(payload) {
		return [][]byte{payload}
	}
	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		restored, _ := restoreClientToolPayload(payload, r.mapping)
		return [][]byte{r.resequence(restored)}
	case "response.output_item.added", "response.output_item.done", "response.function_call_arguments.delta", "response.function_call_arguments.done":
		if r.isClientToolEvent(payload) {
			break
		}
		fallthrough
	default:
		return [][]byte{r.resequence(payload)}
	}

	var event map[string]any
	if err := common.Unmarshal(payload, &event); err != nil {
		return [][]byte{r.resequence(payload)}
	}
	outputIndex := gjson.GetBytes(payload, "output_index").Int()
	var out []map[string]any
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		call := r.recordItem(item, outputIndex)
		if call == nil {
			r.restoreNamespaceItem(item)
			break
		}
		arguments := call.arguments.String()
		if call.kind == "custom" {
			item["type"] = "custom_tool_call"
			item["input"] = ""
			if eventType == "response.output_item.done" {
				item["input"] = extractCustomToolInput(arguments)
			}
			delete(item, "arguments")
			delete(item, "namespace")
		} else {
			if eventType == "response.output_item.added" {
				arguments = ""
			}
			restoreToolSearchItem(item, arguments)
		}
		if call.clientItemID != "" {
			item["id"] = call.clientItemID
		}
		if eventType == "response.output_item.done" {
			delete(r.calls, call.itemID)
			delete(r.calls, call.callID)
			delete(r.byOutput, call.outputIndex)
		}
	case "response.function_call_arguments.delta":
		if call := r.callFor(event, outputIndex); call != nil {
			call.arguments.WriteString(jsonStringValue(event["delta"]))
			return nil
		}
		r.restoreNamespaceName(event)
	case "response.function_call_arguments.done":
		call := r.callFor(event, outputIndex)
		if call == nil {
			r.restoreNamespaceName(event)
			break
		}
		if arguments := jsonStringValue(event["arguments"]); arguments != "" {
			call.arguments.Reset()
			call.arguments.WriteString(arguments)
		}
		if call.kind != "custom" {
			return nil
		}
		input := extractCustomToolInput(call.arguments.String())
		if input != "" {
			out = append(out, map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": call.outputIndex, "item_id": call.clientItemID, "delta": input})
		}
		out = append(out, map[string]any{"type": "response.custom_tool_call_input.done", "output_index": call.outputIndex, "item_id": call.clientItemID, "call_id": call.callID, "name": call.name, "input": input})
		return r.encodeEvents(out)
	}
	return r.encodeEvents([]map[string]any{event})
}

func (r *streamEventRestorer) encodeEvents(events []map[string]any) [][]byte {
	result := make([][]byte, 0, len(events))
	for _, event := range events {
		event["sequence_number"] = r.nextSeq
		r.nextSeq++
		encoded, err := common.Marshal(event)
		if err != nil {
			continue
		}
		result = append(result, encoded)
	}
	return result
}

// resequence keeps opaque upstream payloads byte-identical unless an earlier
// suppression or expansion shifted the sequence numbers.
func (r *streamEventRestorer) resequence(payload []byte) []byte {
	sequence := gjson.GetBytes(payload, "sequence_number")
	if !sequence.Exists() {
		return payload
	}
	if !r.seenSeq {
		r.nextSeq, r.seenSeq = sequence.Int(), true
	}
	if sequence.Int() == r.nextSeq {
		r.nextSeq++
		return payload
	}
	patched, err := sjson.SetBytes(payload, "sequence_number", r.nextSeq)
	r.nextSeq++
	if err != nil {
		return payload
	}
	return patched
}

func (r *streamEventRestorer) isClientToolEvent(payload []byte) bool {
	if !r.seenSeq {
		if sequence := gjson.GetBytes(payload, "sequence_number"); sequence.Exists() {
			r.nextSeq, r.seenSeq = sequence.Int(), true
		}
	}
	if item := gjson.GetBytes(payload, "item"); item.Exists() {
		if item.Get("type").String() != "function_call" {
			return false
		}
		name := item.Get("name").String()
		_, namespaceTool := r.mapping.namespaceTools[name]
		return r.mapping.kindOf(name) != "" || namespaceTool || r.calls[item.Get("id").String()] != nil || r.calls[item.Get("call_id").String()] != nil
	}
	if _, namespaceTool := r.mapping.namespaceTools[gjson.GetBytes(payload, "name").String()]; namespaceTool {
		return true
	}
	return r.calls[gjson.GetBytes(payload, "item_id").String()] != nil ||
		r.calls[gjson.GetBytes(payload, "call_id").String()] != nil ||
		r.byOutput[gjson.GetBytes(payload, "output_index").Int()] != nil
}

func (r *streamEventRestorer) recordItem(item map[string]any, outputIndex int64) *streamToolCall {
	if item == nil || jsonStringValue(item["type"]) != "function_call" {
		return nil
	}
	name := jsonStringValue(item["name"])
	kind := r.mapping.kindOf(name)
	if kind == "" {
		return nil
	}
	itemID := jsonStringValue(item["id"])
	callID := jsonStringValue(item["call_id"])
	key := itemID
	if key == "" {
		key = callID
	}
	call := r.calls[key]
	if call == nil {
		itemType := "custom_tool_call"
		if kind == "search" {
			itemType = "tool_search_call"
		}
		call = &streamToolCall{
			kind:         kind,
			name:         name,
			callID:       callID,
			itemID:       itemID,
			clientItemID: retypeToolCallItemID(itemID, itemType),
			outputIndex:  outputIndex,
		}
		r.calls[key] = call
		if callID != "" {
			r.calls[callID] = call
		}
		r.byOutput[outputIndex] = call
	}
	if arguments := jsonStringValue(item["arguments"]); arguments != "" {
		call.arguments.Reset()
		call.arguments.WriteString(arguments)
	}
	return call
}

func (r *streamEventRestorer) callFor(event map[string]any, outputIndex int64) *streamToolCall {
	if call := r.calls[jsonStringValue(event["item_id"])]; call != nil {
		return call
	}
	if call := r.byOutput[outputIndex]; call != nil {
		return call
	}
	callID := jsonStringValue(event["call_id"])
	name := jsonStringValue(event["name"])
	for _, call := range r.calls {
		if (callID != "" && call.callID == callID) || (jsonStringValue(event["item_id"]) == "" && name != "" && call.name == name) {
			return call
		}
	}
	return nil
}

func (r *streamEventRestorer) restoreNamespaceItem(item map[string]any) {
	if item == nil || jsonStringValue(item["type"]) != "function_call" {
		return
	}
	if entry, ok := r.mapping.namespaceTools[jsonStringValue(item["name"])]; ok {
		item["name"] = entry.Name
		item["namespace"] = entry.Namespace
	}
}

func (r *streamEventRestorer) restoreNamespaceName(event map[string]any) {
	if entry, ok := r.mapping.namespaceTools[jsonStringValue(event["name"])]; ok {
		event["name"] = entry.Name
	}
}
