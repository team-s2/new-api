package groksub

import "strings"

// Grok subscription channel: relays OpenAI Responses-protocol traffic to the
// official Grok CLI gateway (cli-chat-proxy.grok.com) using OAuth tokens from
// the xAI PKCE flow. Upstream identity constants mirror the official Grok CLI
// client; the gateway fingerprints the client string, so inbound client UAs
// must never be forwarded.

const (
	ChannelName = "groksub"

	// cliClientVersion is the pinned Grok CLI version reported upstream.
	// Keep in sync with https://x.ai/cli/stable when bumping.
	cliClientVersion    = "1.0.46"
	cliClientIdentifier = "grok-pager"
	cliClientMode       = "interactive"
	cliTokenAuth        = "xai-grok-cli"
	cliProxyHost        = "cli-chat-proxy.grok.com"

	// DefaultTextModel is the built-in fallback for empty models and the
	// bare "grok" aliases.
	DefaultTextModel = "grok-4.6"
)

// ModelList is the default channel model list (text models only; the gateway
// does not expose Grok Imagine media models).
var ModelList = []string{
	"grok-4.7",
	"grok-4.6",
	"grok-4.5",
	"grok-4.3",
	"grok-build-0.1",
	"grok-composer-2.5-fast",
	"grok-4.20-0309-reasoning",
	"grok-4.20-0309-non-reasoning",
	"grok-4.20-multi-agent-0309",
}

// grokTextModelAliases canonicalizes client-facing aliases (and provider
// prefixes such as xai/) onto upstream model IDs.
var grokTextModelAliases = map[string]string{
	"grok":                         DefaultTextModel,
	"grok-latest":                  DefaultTextModel,
	"grok-4.7":                     "grok-4.7",
	"grok-4.7-latest":              "grok-4.7",
	"grok-4.6":                     "grok-4.6",
	"grok-4.6-latest":              "grok-4.6",
	"grok-4.5":                     "grok-4.5",
	"grok-4.5-latest":              "grok-4.5",
	"grok-4.3":                     "grok-4.3",
	"grok-4.3-latest":              "grok-4.3",
	"grok-3-mini":                  "grok-3-mini",
	"grok-3-mini-fast":             "grok-3-mini-fast",
	"grok-build":                   "grok-build-0.1",
	"grok-build-latest":            "grok-build-0.1",
	"grok-build-0.1":               "grok-build-0.1",
	"grok-composer-2.5-fast":       "grok-composer-2.5-fast",
	"grok-composer":                "grok-composer-2.5-fast",
	"composer-2.5":                 "grok-composer-2.5-fast",
	"grok-4.20-reasoning":          "grok-4.20-0309-reasoning",
	"grok-4.20-0309-reasoning":     "grok-4.20-0309-reasoning",
	"grok-4.20-non-reasoning":      "grok-4.20-0309-non-reasoning",
	"grok-4.20-0309-non-reasoning": "grok-4.20-0309-non-reasoning",
	"grok-4.20-multi-agent":        "grok-4.20-multi-agent-0309",
	"grok-4.20-multi-agent-latest": "grok-4.20-multi-agent-0309",
	"grok-4.20-multi-agent-0309":   "grok-4.20-multi-agent-0309",
}

// stripProviderPrefix removes common provider prefixes callers may attach to
// Grok model names.
func stripProviderPrefix(model string) string {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"xai/", "x-ai/", "grok/"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

// ResolveModelID canonicalizes a Grok text model alias to its upstream ID.
// Unknown IDs are returned with the provider prefix stripped so custom
// channel models keep working.
func ResolveModelID(model string) string {
	trimmed := stripProviderPrefix(model)
	if trimmed == "" {
		return DefaultTextModel
	}
	if canonical, ok := grokTextModelAliases[strings.ToLower(trimmed)]; ok {
		return canonical
	}
	return trimmed
}
