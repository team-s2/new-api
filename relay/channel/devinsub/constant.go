package devinsub

import "github.com/QuantumNous/new-api/common"

const (
	ChannelName = "Devin Subscription"

	// DefaultBaseURL is the upstream Cognition/Codeium gateway the Devin
	// desktop CLI talks to.
	DefaultBaseURL = "https://server.codeium.com"
	// ChatRPCPath is the Connect-RPC endpoint for chat completions.
	ChatRPCPath = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	// SelfDevinSessionTokenRPCPath mints a short-lived session token from a
	// durable cog_* API key (best-effort self-healing path).
	SelfDevinSessionTokenRPCPath = "/exa.seat_management_pb.SeatManagementService/GetSelfDevinSessionToken"
	// UserStatusRPCPath returns plan/quota information.
	UserStatusRPCPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"
	// CliModelConfigsRPCPath returns the account-scoped model catalog.
	CliModelConfigsRPCPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

	// DefaultAPIBaseURL hosts the OAuth token exchange and profile APIs.
	DefaultAPIBaseURL = "https://api.devin.ai"
	TokenPath         = "/auth/cli/token"
	SelfPath          = "/v3/self"

	// ClientName/ClientVersion mirror the devin-cli metadata fingerprint
	// declared in GetChatMessageRequest field 1.
	ClientName           = "chisel"
	ClientVersion        = "3000.10.21"
	fingerprintHexLength = 732

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
