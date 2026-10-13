package codex

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
)

var ModelList = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex-spark",
	"codex-auto-review",
	"gpt-image-2",
}

const ChannelName = "codex"

const (
	codexImageModel = "gpt-image-2"
	// codexOriginator is sent on every codex path (responses, compact, alpha
	// search, images), matching the native codex-tui client.
	codexOriginator = "codex-tui"
	// codexUserAgent mirrors the native codex-tui identity; override with the
	// CODEX_USER_AGENT environment variable.
	codexUserAgent = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
)

// CodexUserAgent returns the User-Agent for codex upstream requests,
// preferring the CODEX_USER_AGENT environment override.
func CodexUserAgent() string {
	if override := strings.TrimSpace(common.CodexUserAgentOverride); override != "" {
		return override
	}
	return codexUserAgent
}

// CodexSessionUUID derives the stable per-token session id used for
// prompt_cache_key and the session_id header when the client did not send
// either. Keying on the token keeps the value stable across requests of one
// client while isolating different clients, the same derivation shape
// CLIProxyAPI uses (uuid.NewSHA1 over a namespaced identity).
func CodexSessionUUID(tokenKey string) string {
	tokenKey = strings.TrimSpace(tokenKey)
	if tokenKey == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("new-api:codex:prompt-cache:%s", tokenKey))).String()
}

func isCodexResponsesLiteModel(modelName string) bool {
	switch strings.TrimSpace(modelName) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	default:
		return false
	}
}
