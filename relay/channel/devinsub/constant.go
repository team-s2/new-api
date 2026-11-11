package devinsub

import "github.com/QuantumNous/new-api/common"

const (
	ChannelName = "Devin Subscription"

	// DefaultBaseURL is the upstream Cognition/Codeium gateway the Devin
	// desktop CLI talks to.
	DefaultBaseURL = "https://server.codeium.com"
	// ChatRPCPath is the Connect-RPC endpoint for chat completions.
	ChatRPCPath      = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	defaultMaxTokens = 128000
)

// ModelList exposes only the two SWE-2 families. Bare names alias to the
// high-effort tier, matching devin-cli defaults.
var ModelList = common.DevinModels()

// ResolveModelUID maps a client-facing model name to the upstream
// chat_model_uid using the same exact catalog as account model discovery.
func ResolveModelUID(model string) (string, bool) {
	return common.ResolveDevinModelUID(model)
}
