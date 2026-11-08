package devinsub

// Connect-RPC + protobuf wire layer for the Devin (Codeium) GetChatMessage
// API. The field layout is reverse engineered from devin-cli; the codec is a
// reduced port of the MIT-licensed reference implementation in
// router-for-me/CLIProxyAPI (internal/helpers/devin_wire.go) using
// google.golang.org/protobuf/encoding/protowire — no generated bindings.

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	connectFlagData       byte = 0x00
	connectFlagCompressed byte = 0x01
	connectFlagEndStream  byte = 0x02

	maxConnectFrameSize      = 16 * 1024 * 1024
	maxDecompressedFrameSize = 64 * 1024 * 1024
)

// devinTool is a tool definition in GetChatMessageRequest field 10.
type devinTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  []byte `json:"parameters,omitempty"`
}

// devinToolChoice is the oneof selection in request field 12.
type devinToolChoice struct {
	OptionName string `json:"option_name,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

func parseDevinToolChoice(value any, tools []devinTool) (*devinToolChoice, error) {
	if value == nil {
		return nil, nil
	}
	if option, ok := value.(string); ok {
		switch option {
		case "none", "auto", "required":
			return &devinToolChoice{OptionName: option}, nil
		default:
			return nil, fmt.Errorf("devin subscription channel: unsupported tool_choice %q", option)
		}
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("devin subscription channel: invalid tool_choice: %w", err)
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := common.Unmarshal(encoded, &choice); err != nil {
		return nil, fmt.Errorf("devin subscription channel: invalid tool_choice: %w", err)
	}
	if choice.Type == "function" && choice.Function.Name != "" {
		for _, tool := range tools {
			if tool.Name == choice.Function.Name {
				return &devinToolChoice{ToolName: tool.Name}, nil
			}
		}
	}
	return nil, fmt.Errorf("devin subscription channel: tool_choice must select a declared function")
}

// devinToolCall is a completed call embedded in an assistant prompt.
type devinToolCall struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// devinToolCallDelta is a streaming tool call chunk from response field 6.
type devinToolCallDelta struct {
	ID               string
	Name             string
	Arguments        string
	InvalidJSONStr   string
	InvalidJSONErr   string
	IsCustomToolCall bool
}

// devinImage is a base64 image attachment in a prompt (field 10).
type devinImage struct {
	Base64Data string `json:"base64_data"`
	MimeType   string `json:"mime_type,omitempty"`
}

// devinPrompt is one turn in the request history (repeated field 3).
type devinPrompt struct {
	MessageID  string          `json:"message_id,omitempty"`
	Source     int             `json:"source"` // 1=user, 2=assistant, 4=tool
	Content    string          `json:"content,omitempty"`
	Images     []devinImage    `json:"images,omitempty"`
	ToolCalls  []devinToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
}

// devinUsage is token accounting from response field 7.
type devinUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheWriteTokens int64
	StatusCode       uint64
	RequestID        string
	ModelName        string
}

// devinFrameResult is the decoded content of a single response frame.
type devinFrameResult struct {
	OutputID           string
	Timestamp          uint64
	ContentText        string
	StopReason         uint64 // 2/4=stop, 10=tool_calls
	ToolCallDeltas     []devinToolCallDelta
	ThinkingText       string
	DeltaSignature     []byte
	DeltaSignatureType string
	MessageID          string
	Usage              *devinUsage
}

// generateDeviceFingerprint produces the mandatory 732-hex-char device
// fingerprint (metadata field 1.31). Random per request, matching devin-cli.
func generateDeviceFingerprint() string {
	var b [fingerprintHexLength / 2]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	seed := uuid.New().String()
	var sb strings.Builder
	for counter := 0; sb.Len() < fingerprintHexLength; counter++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
		sb.WriteString(hex.EncodeToString(h[:]))
	}
	return sb.String()[:fingerprintHexLength]
}

// generateSentryTrace mirrors the devin-cli distributed tracing header:
// "<32-hex-trace-id>-<16-hex-span-id>-1".
func generateSentryTrace() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		u1 := strings.ReplaceAll(uuid.New().String(), "-", "")
		u2 := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
		return u1 + "-" + u2 + "-1"
	}
	return hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:24]) + "-1"
}

// Per-session turn index (request field 15.2): 0-based request ordinal for a
// cascade session. Sessions are short-lived here (one HTTP request = one
// session unless the client pins prompt_cache_key), so a small capped map is
// enough.
var sessionTurnCounters = struct {
	sync.Mutex
	m map[string]*atomic.Uint64
}{m: make(map[string]*atomic.Uint64)}

func nextSessionTurnIndex(sessionID string) int {
	if strings.TrimSpace(sessionID) == "" {
		return 0
	}
	sessionTurnCounters.Lock()
	if len(sessionTurnCounters.m) > 10000 {
		sessionTurnCounters.m = make(map[string]*atomic.Uint64)
	}
	counter, ok := sessionTurnCounters.m[sessionID]
	if !ok {
		counter = &atomic.Uint64{}
		sessionTurnCounters.m[sessionID] = counter
	}
	sessionTurnCounters.Unlock()
	return int(counter.Add(1) - 1)
}

// wrapConnectEnvelope wraps payload bytes into a 5-byte Connect frame:
// [flag:1][len:4 BE][payload].
func wrapConnectEnvelope(flag byte, payload []byte) []byte {
	header := make([]byte, 5, 5+len(payload))
	header[0] = flag
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
	return append(header, payload...)
}

// readConnectFrame reads and decompresses one Connect frame.
func readConnectFrame(r io.Reader) (flag byte, payload []byte, err error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	flag = header[0]
	if flag != connectFlagData && flag != connectFlagCompressed &&
		flag != connectFlagEndStream && flag != (connectFlagCompressed|connectFlagEndStream) {
		return flag, nil, fmt.Errorf("invalid connect frame flag: 0x%02x", flag)
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length > maxConnectFrameSize {
		return flag, nil, fmt.Errorf("connect frame length %d exceeds maximum limit (%d)", length, maxConnectFrameSize)
	}

	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if flag&connectFlagCompressed != 0 {
		gz, errGz := gzip.NewReader(bytes.NewReader(payload))
		if errGz != nil {
			return flag, nil, fmt.Errorf("decompress gzip connect frame: %w", errGz)
		}
		defer func() { _ = gz.Close() }()

		initCap := int(length) * 4
		if initCap > 4*1024*1024 {
			initCap = 4 * 1024 * 1024
		} else if initCap < 4096 {
			initCap = 4096
		}
		decompBuf := bytes.NewBuffer(make([]byte, 0, initCap))
		limitedReader := io.LimitReader(gz, maxDecompressedFrameSize+1)
		if _, errRead := decompBuf.ReadFrom(limitedReader); errRead != nil {
			return flag, nil, fmt.Errorf("read decompressed connect frame: %w", errRead)
		}
		if decompBuf.Len() > maxDecompressedFrameSize {
			return flag, nil, fmt.Errorf("decompressed frame size exceeds maximum limit (%d)", maxDecompressedFrameSize)
		}
		payload = decompBuf.Bytes()
	}
	return flag, payload, nil
}

// buildClientMetadataBytes serializes request field 1 (ClientMetadata).
func buildClientMetadataBytes(sessionToken string) []byte {
	var f1Bytes []byte
	f1Bytes = protowire.AppendTag(f1Bytes, 1, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, ClientName)
	f1Bytes = protowire.AppendTag(f1Bytes, 2, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, ClientVersion)
	f1Bytes = protowire.AppendTag(f1Bytes, 3, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, sessionToken)
	f1Bytes = protowire.AppendTag(f1Bytes, 4, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "en")
	f1Bytes = protowire.AppendTag(f1Bytes, 5, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, runtime.GOOS)
	f1Bytes = protowire.AppendTag(f1Bytes, 7, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, ClientVersion)
	f1Bytes = protowire.AppendTag(f1Bytes, 12, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, ClientName)
	f1Bytes = protowire.AppendTag(f1Bytes, 31, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, generateDeviceFingerprint())
	return f1Bytes
}

// buildGetChatMessageRequest encodes a full GetChatMessageRequest payload.
func buildGetChatMessageRequest(
	sessionToken string,
	chatModelUID string,
	systemPrompt string,
	prompts []devinPrompt,
	tools []devinTool,
	toolChoice *devinToolChoice,
	temperature *float64,
	maxTokens int,
	sessionID string,
	cascadeID string,
) []byte {
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	if cascadeID == "" {
		cascadeID = sessionID
	}

	estimatedSize := 4096 + len(systemPrompt)
	for _, p := range prompts {
		estimatedSize += 256 + len(p.Content)
	}
	reqBytes := make([]byte, 0, estimatedSize)

	// Field 1: client metadata.
	f1Bytes := buildClientMetadataBytes(sessionToken)
	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f1Bytes)

	// Field 2: system prompt.
	if strings.TrimSpace(systemPrompt) != "" {
		reqBytes = protowire.AppendTag(reqBytes, 2, protowire.BytesType)
		reqBytes = protowire.AppendString(reqBytes, systemPrompt)
	}

	// Field 3: repeated history prompts.
	for _, p := range prompts {
		var pBytes []byte

		msgID := p.MessageID
		if msgID == "" {
			msgID = uuid.New().String()
		}
		pBytes = protowire.AppendTag(pBytes, 1, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, msgID)

		source := p.Source
		if source <= 0 {
			source = 1
		}
		pBytes = protowire.AppendTag(pBytes, 2, protowire.VarintType)
		pBytes = protowire.AppendVarint(pBytes, uint64(source))

		pBytes = protowire.AppendTag(pBytes, 3, protowire.BytesType)
		pBytes = protowire.AppendString(pBytes, p.Content)

		for _, tc := range p.ToolCalls {
			var tcBytes []byte
			if tc.ID != "" {
				tcBytes = protowire.AppendTag(tcBytes, 1, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.ID)
			}
			if tc.Name != "" {
				tcBytes = protowire.AppendTag(tcBytes, 2, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Name)
			}
			if tc.Arguments != "" {
				tcBytes = protowire.AppendTag(tcBytes, 3, protowire.BytesType)
				tcBytes = protowire.AppendString(tcBytes, tc.Arguments)
			}
			pBytes = protowire.AppendTag(pBytes, 6, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, tcBytes)
		}

		if p.ToolCallID != "" {
			pBytes = protowire.AppendTag(pBytes, 7, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.ToolCallID)
		}

		for _, img := range p.Images {
			data := strings.TrimSpace(img.Base64Data)
			if data == "" {
				continue
			}
			var imgBytes []byte
			imgBytes = protowire.AppendTag(imgBytes, 1, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, data)
			mime := strings.TrimSpace(img.MimeType)
			if mime == "" {
				mime = "image/png"
			}
			imgBytes = protowire.AppendTag(imgBytes, 2, protowire.BytesType)
			imgBytes = protowire.AppendString(imgBytes, mime)
			pBytes = protowire.AppendTag(pBytes, 10, protowire.BytesType)
			pBytes = protowire.AppendBytes(pBytes, imgBytes)
		}

		if p.Thinking != "" {
			pBytes = protowire.AppendTag(pBytes, 11, protowire.BytesType)
			pBytes = protowire.AppendString(pBytes, p.Thinking)
		}

		reqBytes = protowire.AppendTag(reqBytes, 3, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, pBytes)
	}

	// Field 7: request_type = CASCADE (5), the only stateless entry point.
	reqBytes = protowire.AppendTag(reqBytes, 7, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 5)

	// Field 8: completion configuration.
	var f8Bytes []byte
	f8Bytes = protowire.AppendTag(f8Bytes, 1, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 1)
	f8Bytes = protowire.AppendTag(f8Bytes, 2, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, uint64(maxTokens))
	f8Bytes = protowire.AppendTag(f8Bytes, 3, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 400)
	tempVal := 1.0
	if temperature != nil {
		tempVal = *temperature
	}
	f8Bytes = protowire.AppendTag(f8Bytes, 5, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(tempVal))
	f8Bytes = protowire.AppendTag(f8Bytes, 7, protowire.VarintType)
	f8Bytes = protowire.AppendVarint(f8Bytes, 40)
	f8Bytes = protowire.AppendTag(f8Bytes, 8, protowire.Fixed64Type)
	f8Bytes = protowire.AppendFixed64(f8Bytes, math.Float64bits(float64(float32(0.95))))
	reqBytes = protowire.AppendTag(reqBytes, 8, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f8Bytes)

	// Field 10: repeated tools.
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		var tBytes []byte
		tBytes = protowire.AppendTag(tBytes, 1, protowire.BytesType)
		tBytes = protowire.AppendString(tBytes, tool.Name)
		if tool.Description != "" {
			tBytes = protowire.AppendTag(tBytes, 2, protowire.BytesType)
			tBytes = protowire.AppendString(tBytes, tool.Description)
		}
		if len(tool.Parameters) > 0 {
			tBytes = protowire.AppendTag(tBytes, 3, protowire.BytesType)
			tBytes = protowire.AppendBytes(tBytes, tool.Parameters)
		}
		reqBytes = protowire.AppendTag(reqBytes, 10, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, tBytes)
	}

	// Field 12: tool choice (option_name=1 or tool_name=2).
	if toolChoice != nil {
		var choiceBytes []byte
		if toolChoice.ToolName != "" {
			choiceBytes = protowire.AppendTag(choiceBytes, 2, protowire.BytesType)
			choiceBytes = protowire.AppendString(choiceBytes, toolChoice.ToolName)
		} else {
			choiceBytes = protowire.AppendTag(choiceBytes, 1, protowire.BytesType)
			choiceBytes = protowire.AppendString(choiceBytes, toolChoice.OptionName)
		}
		reqBytes = protowire.AppendTag(reqBytes, 12, protowire.BytesType)
		reqBytes = protowire.AppendBytes(reqBytes, choiceBytes)
	}

	// Field 15: thread session metadata.
	turnIndex := nextSessionTurnIndex(sessionID)
	var f15Bytes []byte
	f15Bytes = protowire.AppendTag(f15Bytes, 1, protowire.BytesType)
	f15Bytes = protowire.AppendString(f15Bytes, sessionID)
	if turnIndex > 0 {
		f15Bytes = protowire.AppendTag(f15Bytes, 2, protowire.VarintType)
		f15Bytes = protowire.AppendVarint(f15Bytes, uint64(turnIndex))
	}
	f15Bytes = protowire.AppendTag(f15Bytes, 3, protowire.VarintType)
	f15Bytes = protowire.AppendVarint(f15Bytes, 4)
	if len(prompts) > 0 && prompts[len(prompts)-1].Source == 1 {
		if turnIndex == 0 || len(prompts) < 2 || prompts[len(prompts)-2].Source != 1 {
			f15Bytes = protowire.AppendTag(f15Bytes, 4, protowire.VarintType)
			f15Bytes = protowire.AppendVarint(f15Bytes, 14)
		}
	}
	reqBytes = protowire.AppendTag(reqBytes, 15, protowire.BytesType)
	reqBytes = protowire.AppendBytes(reqBytes, f15Bytes)

	// Field 16: cascade id (session-stable prompt cache key).
	reqBytes = protowire.AppendTag(reqBytes, 16, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, cascadeID)

	// Field 20: fixed flag.
	reqBytes = protowire.AppendTag(reqBytes, 20, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, 1)

	// Field 21: chat_model_uid (required).
	reqBytes = protowire.AppendTag(reqBytes, 21, protowire.BytesType)
	reqBytes = protowire.AppendString(reqBytes, chatModelUID)

	return reqBytes
}

// parseDevinFrame decodes one response frame's payload.
func parseDevinFrame(payload []byte) (devinFrameResult, error) {
	var res devinFrameResult
	var textParts []string
	var thinkingParts []string

	pos := 0
	for pos < len(payload) {
		num, typ, n := protowire.ConsumeTag(payload[pos:])
		if n <= 0 {
			return res, fmt.Errorf("consume tag error at offset %d: %w", pos, protowire.ParseError(n))
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(payload[pos:])
			if vn <= 0 {
				return res, fmt.Errorf("consume varint error at offset %d: %w", pos, protowire.ParseError(vn))
			}
			pos += vn
			switch num {
			case 2:
				res.Timestamp = v
			case 5:
				res.StopReason = v
			}

		case protowire.Fixed64Type:
			_, fn := protowire.ConsumeFixed64(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed64 error at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn

		case protowire.Fixed32Type:
			_, fn := protowire.ConsumeFixed32(payload[pos:])
			if fn <= 0 {
				return res, fmt.Errorf("consume fixed32 error at offset %d: %w", pos, protowire.ParseError(fn))
			}
			pos += fn

		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(payload[pos:])
			if bn <= 0 {
				return res, fmt.Errorf("consume bytes error at offset %d: %w", pos, protowire.ParseError(bn))
			}
			pos += bn

			switch num {
			case 1:
				res.OutputID = string(val)
			case 3:
				textParts = append(textParts, string(val))
			case 6:
				if tc, err := parseDevinToolCallDelta(val); err == nil {
					res.ToolCallDeltas = append(res.ToolCallDeltas, tc)
				}
			case 7:
				res.Usage = parseDevinUsageField(val)
			case 9:
				thinkingParts = append(thinkingParts, string(val))
			case 10:
				res.DeltaSignature = append(res.DeltaSignature, val...)
			case 17:
				res.MessageID = string(val)
			case 21:
				res.DeltaSignatureType = string(val)
			}
		default:
			nSkip := protowire.ConsumeFieldValue(num, typ, payload[pos:])
			if nSkip <= 0 {
				return res, fmt.Errorf("unsupported wire type %d at offset %d", typ, pos)
			}
			pos += nSkip
		}
	}

	res.ContentText = strings.Join(textParts, "")
	res.ThinkingText = strings.Join(thinkingParts, "")
	return res, nil
}

func parseDevinToolCallDelta(data []byte) (devinToolCallDelta, error) {
	var tc devinToolCallDelta
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			return tc, protowire.ParseError(n)
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return tc, protowire.ParseError(vn)
			}
			pos += vn
			if num == 6 {
				tc.IsCustomToolCall = v != 0
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return tc, protowire.ParseError(bn)
			}
			pos += bn
			switch num {
			case 1:
				tc.ID = string(val)
			case 2:
				tc.Name = string(val)
			case 3:
				tc.Arguments = string(val)
			case 4:
				tc.InvalidJSONStr = string(val)
			case 5:
				tc.InvalidJSONErr = string(val)
			}
		default:
			nSkip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if nSkip <= 0 {
				return tc, protowire.ParseError(nSkip)
			}
			pos += nSkip
		}
	}
	return tc, nil
}

// parseDevinHeaderField parses a repeated submessage in usage field 7
// (subfield 8) representing upstream response headers.
func parseDevinHeaderField(data []byte) (string, string) {
	var key, val string
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n
		if typ == protowire.BytesType {
			b, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return key, val
			}
			pos += bn
			switch num {
			case 1:
				key = string(b)
			case 2:
				val = string(b)
			}
		} else {
			nSkip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if nSkip <= 0 {
				return key, val
			}
			pos += nSkip
		}
	}
	return key, val
}

func parseDevinUsageField(data []byte) *devinUsage {
	u := &devinUsage{}
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n

		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return u
			}
			pos += vn
			switch num {
			case 2: // uncached input tokens
				u.PromptTokens += int64(v)
			case 3: // output tokens
				u.CompletionTokens = int64(v)
			case 4: // cache write tokens
				u.CacheWriteTokens += int64(v)
			case 5: // cache read tokens
				u.CachedTokens = int64(v)
			case 6: // status code
				u.StatusCode = v
			}
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return u
			}
			pos += bn
			switch num {
			case 8:
				k, v := parseDevinHeaderField(val)
				if k != "" {
					if (strings.EqualFold(k, "x-request-id") || strings.EqualFold(k, "request-id")) && v != "" {
						u.RequestID = v
					}
				} else if len(val) > 0 && isPrintableASCII(val) && u.RequestID == "" {
					u.RequestID = string(val)
				}
			case 9:
				u.ModelName = string(val)
			}
		default:
			nSkip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if nSkip <= 0 {
				return u
			}
			pos += nSkip
		}
	}
	return u
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// parseDevinTrailerError inspects Connect-RPC end-stream trailer frames and
// maps the embedded error to an HTTP-like status code.
func parseDevinTrailerError(payload []byte) (statusCode int, err error) {
	trimmed := bytes.TrimSpace(payload)
	if bytes.Equal(trimmed, []byte("{}")) {
		return 0, nil
	}
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return 502, fmt.Errorf("devin upstream: invalid end-stream trailer")
	}
	if err := common.Unmarshal(trimmed, &trailer); err != nil {
		return 502, fmt.Errorf("devin upstream: invalid end-stream trailer: %w", err)
	}
	if trailer.Error == nil {
		return 0, nil
	}

	codeStr := strings.ToLower(trailer.Error.Code)
	msgLower := strings.ToLower(trailer.Error.Message)

	httpCode := 502
	switch codeStr {
	case "invalid_argument":
		if strings.Contains(msgLower, "internal error") {
			httpCode = 502
		} else {
			httpCode = 400
		}
	case "internal":
		httpCode = 502
	case "unauthenticated":
		httpCode = 401
	case "permission_denied":
		if strings.Contains(msgLower, "high demand") {
			httpCode = 429
		} else {
			httpCode = 403
		}
	case "resource_exhausted":
		httpCode = 429
	case "unavailable":
		httpCode = 503
	case "canceled":
		httpCode = 499
	case "deadline_exceeded":
		httpCode = 504
	case "failed_precondition":
		if strings.Contains(msgLower, "quota") ||
			strings.Contains(msgLower, "credit") ||
			strings.Contains(msgLower, "acu") ||
			strings.Contains(msgLower, "exhausted") ||
			strings.Contains(msgLower, "limit") {
			httpCode = 429
		} else {
			httpCode = 400
		}
	}
	return httpCode, fmt.Errorf("devin upstream error (%s): %s", trailer.Error.Code, trailer.Error.Message)
}

// decodeWireFields generically decodes a protobuf message into a
// field-number → values map. Used by the usage/model-catalog probes whose
// schemas are large and version-tolerant; callers interpret what they need.
func decodeWireFields(data []byte) map[int][][]byte {
	out := make(map[int][][]byte)
	pos := 0
	for pos < len(data) {
		num, typ, n := protowire.ConsumeTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n
		switch typ {
		case protowire.BytesType:
			val, bn := protowire.ConsumeBytes(data[pos:])
			if bn <= 0 {
				return out
			}
			pos += bn
			cp := make([]byte, len(val))
			copy(cp, val)
			out[int(num)] = append(out[int(num)], cp)
		default:
			var buf [binary.MaxVarintLen64]byte
			var raw []byte
			switch typ {
			case protowire.VarintType:
				v, vn := protowire.ConsumeVarint(data[pos:])
				if vn <= 0 {
					return out
				}
				raw = protowire.AppendVarint(buf[:0], v)
				pos += vn
			case protowire.Fixed32Type:
				v, vn := protowire.ConsumeFixed32(data[pos:])
				if vn <= 0 {
					return out
				}
				raw = protowire.AppendFixed32(buf[:0], v)
				pos += vn
			case protowire.Fixed64Type:
				v, vn := protowire.ConsumeFixed64(data[pos:])
				if vn <= 0 {
					return out
				}
				raw = protowire.AppendFixed64(buf[:0], v)
				pos += vn
			default:
				nSkip := protowire.ConsumeFieldValue(num, typ, data[pos:])
				if nSkip <= 0 {
					return out
				}
				pos += nSkip
				continue
			}
			out[int(num)] = append(out[int(num)], raw)
		}
	}
	return out
}

// utf8SplitBuffer buffers incomplete UTF-8 sequences across chunk boundaries.
type utf8SplitBuffer struct {
	remainder []byte
}

func (b *utf8SplitBuffer) feed(chunk []byte) string {
	combined := append(b.remainder, chunk...)
	b.remainder = nil
	if len(combined) == 0 {
		return ""
	}
	validUntil := 0
	for validUntil < len(combined) {
		r, size := utf8.DecodeRune(combined[validUntil:])
		if r == utf8.RuneError && size == 1 {
			trailingLen := len(combined) - validUntil
			if trailingLen < utf8.UTFMax && !utf8.FullRune(combined[validUntil:]) {
				break
			}
			validUntil++
			continue
		}
		validUntil += size
	}
	validBytes := combined[:validUntil]
	b.remainder = append(b.remainder, combined[validUntil:]...)
	return string(validBytes)
}
