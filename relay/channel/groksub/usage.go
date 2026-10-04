package groksub

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// xAI can exclude reasoning from output_tokens while including it in total_tokens.
// Normalize at the wire boundary so client usage, conversion snapshots, and local
// settlement all see the same output count, before shared handlers recompute totals.
func normalizeGrokUsage(payload []byte, path string) ([]byte, error) {
	raw := gjson.GetBytes(payload, path)
	if !raw.Exists() || raw.Type == gjson.Null {
		return payload, nil
	}
	var usage dto.Usage
	if err := common.Unmarshal([]byte(raw.Raw), &usage); err != nil {
		return nil, err
	}
	if usage.OutputTokensDetails == nil {
		return payload, nil
	}
	reasoning := usage.OutputTokensDetails.ReasoningTokens
	input, output, total := usage.InputTokens, usage.OutputTokens, usage.TotalTokens
	// Subtract only after validating non-negative operands and their ordering;
	// output+extra stays bounded by total-input, even at the integer boundary.
	if reasoning <= 0 || input < 0 || output < 0 || total < input || total-input <= output {
		return payload, nil
	}
	extra := min(reasoning, total-input-output)
	patched, err := sjson.SetBytes(payload, path+".output_tokens", output+extra)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(patched, path+".total_tokens", input+output+extra)
}
