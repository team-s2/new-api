package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/devinwire"
	"google.golang.org/protobuf/encoding/protowire"
)

// Unary Connect-RPC helpers for Devin session tokens, status and model catalogs.
// Client metadata is shared with the chat relay through the leaf devinwire package.

const (
	devinDefaultAPIBaseURL = "https://api.devin.ai"
	devinSeatServicePath   = "/exa.seat_management_pb.SeatManagementService"
	devinAPIServicePath    = "/exa.api_server_pb.ApiServerService"
)

// buildDevinMetadataRequest encodes `{ optional Metadata metadata = 1; }`.
func buildDevinMetadataRequest(token string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, devinwire.Metadata(token))
	return b
}

// postDevinConnectRPC issues a unary Connect-RPC call (application/proto, raw
// protobuf body — the seat/catalog/token RPCs are unary and reject the
// connect+proto streaming envelope with 415). Returns the response message
// bytes; upstream error bodies carry a JSON {code,message}.
func postDevinConnectRPC(ctx context.Context, endpoint, token, proxyURL string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
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

	// A 307/308 redirect would replay the credential-bearing protobuf body,
	// even if net/http strips Authorization at the new origin.
	requestClient := *client
	requestClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
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
