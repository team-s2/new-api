package groksub

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeGrokUsage folds independently reported reasoning tokens into
// output_tokens. xAI can exclude reasoning from output_tokens while including
// it in total_tokens (input=32, output=9, reasoning=110, total=151); OpenAI's
// contract already counts reasoning inside output. Normalizing at the wire
// boundary keeps client usage, conversions, and settlement consistent. The
// payload is returned unchanged when usage is absent or not foldable.
func normalizeGrokUsage(payload []byte, path string) []byte {
	raw := gjson.GetBytes(payload, path)
	if !raw.IsObject() {
		return payload
	}
	var usage dto.Usage
	if err := common.Unmarshal([]byte(raw.Raw), &usage); err != nil || usage.OutputTokensDetails == nil {
		return payload
	}
	reasoning := usage.OutputTokensDetails.ReasoningTokens
	input, output, total := usage.InputTokens, usage.OutputTokens, usage.TotalTokens
	// Subtract only after validating non-negative operands and their ordering;
	// output+extra stays bounded by total-input, even at the integer boundary.
	if reasoning <= 0 || input < 0 || output < 0 || total < input || total-input <= output {
		return payload
	}
	extra := min(reasoning, total-input-output)
	patched, err := sjson.SetBytes(payload, path+".output_tokens", output+extra)
	if err != nil {
		return payload
	}
	patched, err = sjson.SetBytes(patched, path+".total_tokens", input+output+extra)
	if err != nil {
		return payload
	}
	return patched
}
