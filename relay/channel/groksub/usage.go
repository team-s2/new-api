package groksub

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// adaptGrokUsage applies xAI-specific usage semantics: xAI reports visible
// output tokens and reasoning tokens separately, while OpenAI semantics fold
// reasoning into completion tokens. When total != input + output the reasoning
// tokens were NOT folded, so add them back (bounded by the unaccounted
// remainder) to keep billing consistent with OpenAI-compatible clients.
func adaptGrokUsage(usage any) any {
	u, ok := usage.(*dto.Usage)
	if !ok || u == nil {
		return usage
	}
	reasoning := u.CompletionTokenDetails.ReasoningTokens
	if reasoning <= 0 {
		return usage
	}
	input := u.PromptTokens
	output := u.CompletionTokens
	if u.TotalTokens != input+output && u.TotalTokens > input+output {
		unaccounted := u.TotalTokens - input - output
		add := reasoning
		if add > unaccounted {
			add = unaccounted
		}
		u.CompletionTokens += add
		u.TotalTokens = input + u.CompletionTokens
	}
	return u
}
