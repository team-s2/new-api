package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

// Minimal Connect-RPC helpers for the service-side Devin calls (session token
// mint, user status, model catalog). Duplicated from relay/channel/devinsub
// because service importing the relay adaptor would create an import cycle.

const (
	devinConnectClientName    = "chisel"
	devinConnectClientVersion = "3000.10.21"
	devinFingerprintHexLength = 732
	devinDefaultAPIBaseURL    = "https://api.devin.ai"
	devinSeatServicePath      = "/exa.seat_management_pb.SeatManagementService"
	devinAPIServicePath       = "/exa.api_server_pb.ApiServerService"
)

// devinMetadataBytes builds the ExaCodeiumCommonPb_Metadata submessage shared
// by every Devin RPC (field layout mirrors relay/channel/devinsub).
func devinMetadataBytes(token string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, devinConnectClientName)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, devinConnectClientVersion)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, token)
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendString(b, "en")
	b = protowire.AppendTag(b, 5, protowire.BytesType)
	b = protowire.AppendString(b, runtime.GOOS)
	b = protowire.AppendTag(b, 7, protowire.BytesType)
	b = protowire.AppendString(b, devinConnectClientVersion)
	b = protowire.AppendTag(b, 12, protowire.BytesType)
	b = protowire.AppendString(b, devinConnectClientName)
	b = protowire.AppendTag(b, 31, protowire.BytesType)
	b = protowire.AppendString(b, devinFingerprint())
	return b
}

func devinFingerprint() string {
	raw := make([]byte, devinFingerprintHexLength/2)
	if _, err := rand.Read(raw); err != nil {
		return uuid.New().String() + strings.Repeat("0", devinFingerprintHexLength-36)
	}
	return hex.EncodeToString(raw)
}

// buildDevinMetadataRequest encodes `{ optional Metadata metadata = 1; }`.
func buildDevinMetadataRequest(token string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, devinMetadataBytes(token))
	return b
}

// postDevinConnectRPC issues a unary Connect-RPC call (application/proto, raw
// protobuf body — the seat/catalog/token RPCs are unary and reject the
// connect+proto streaming envelope with 415). Returns the response message
// bytes; upstream error bodies carry a JSON {code,message}.
func postDevinConnectRPC(ctx context.Context, endpoint, token, proxyURL string, payload []byte) ([]byte, error) {
	client, err := GetHttpClientWithProxy(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Basic "+token+"-"+token)
	req.Header["User-Agent"] = []string{""}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("devin upstream: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var connErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if jsonErr := common.Unmarshal(bytes.TrimSpace(body), &connErr); jsonErr == nil && connErr.Code != "" {
			return nil, fmt.Errorf("devin upstream error (%s): %s", connErr.Code, connErr.Message)
		}
		return nil, fmt.Errorf("devin upstream returned status %d", resp.StatusCode)
	}
	return body, nil
}

func devinSeatTokenRPCPath(baseURL string) string {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://server.codeium.com"
	}
	return base + devinSeatServicePath + "/GetSelfDevinSessionToken"
}

// extractDevinSessionToken scans a GetSelfDevinSessionTokenResponse payload
// for the session token string (field 1).
func extractDevinSessionToken(payload []byte) string {
	pos := 0
	for pos < len(payload) {
		num, typ, n := protowire.ConsumeTag(payload[pos:])
		if n <= 0 {
			break
		}
		pos += n
		if typ != protowire.BytesType {
			nSkip := protowire.ConsumeFieldValue(num, typ, payload[pos:])
			if nSkip <= 0 {
				break
			}
			pos += nSkip
			continue
		}
		val, bn := protowire.ConsumeBytes(payload[pos:])
		if bn <= 0 {
			break
		}
		pos += bn
		s := string(val)
		if strings.Contains(s, "devin-session-token$") {
			return s
		}
		if num == 1 && len(s) > 20 {
			return NormalizeDevinSessionToken(s)
		}
	}
	return ""
}

// devinDecodeWireFields decodes a protobuf message into field-number →
// raw values for the version-tolerant status/catalog probes.
func devinDecodeWireFields(data []byte) map[int][][]byte {
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
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(data[pos:])
			if vn <= 0 {
				return out
			}
			pos += vn
			out[int(num)] = append(out[int(num)], protowire.AppendVarint(nil, v))
		default:
			nSkip := protowire.ConsumeFieldValue(num, typ, data[pos:])
			if nSkip <= 0 {
				return out
			}
			pos += nSkip
		}
	}
	return out
}

func devinConsumeVarintValue(raw []byte) uint64 {
	v, _ := protowire.ConsumeVarint(raw)
	return v
}
