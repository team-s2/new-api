package opencodego

// OpenCode Go serves every text model through four native protocol endpoints.
// The channel keeps the client's protocol intact and only rewrites the path, so
// no request or response conversion is needed. Endpoints are relative to the
// channel base URL (default https://opencode.ai/inference/go):
//
//	OpenAI Chat Completions  /openai/v1/chat/completions
//	OpenAI Responses         /openai/v1/responses
//	Anthropic Messages       /anthropic/v1/messages
//	Google generateContent   /google/v1beta/models/{model}:generateContent
const (
	ChannelName = "opencode_go"

	openAIChatCompletionsPath = "/openai/v1/chat/completions"
	openAIResponsesPath       = "/openai/v1/responses"
	anthropicMessagesPath     = "/anthropic/v1/messages"
	googleModelsPath          = "/google/v1beta/models/"
)

// ModelList is the subscription's published catalog. It is only a fallback for
// callers that have not discovered the account-specific /models listing; the
// channel's own model list comes from that endpoint.
var ModelList = []string{
	"grok-4.7",
	"glm-5.3-flash",
	"glm-5.3",
	"glm-5.2",
	"gpt-6-luna",
	"gpt-5.6-luna",
	"claude-haiku-5-5",
	"gemini-4-argon",
	"kimi-k3.1-pro",
	"kimi-k3",
	"kimi-k2.7-code",
	"longcat-2.0",
	"step-5-preview",
	"mimo-v2.6-flash",
	"mimo-v2.6-pro",
	"mimo-v2.5",
	"mimo-v2.5-pro",
	"minimax-m3.1-flash",
	"minimax-m3",
	"qwen3.8-max",
	"qwen3.8-flash",
	"qwen3.7-plus",
	"deepseek-v4.1-flash",
	"deepseek-v4-pro",
	"hy4",
	"hy4-preview",
	"hy3",
	"muse-spark-1.3-contributor",
	"muse-spark-1.2-contributor",
	"beam-free",
	"iceman-contributor-free",
	"longcat-2.5-free",
}
