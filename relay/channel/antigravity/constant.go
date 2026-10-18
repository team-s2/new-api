package antigravity

import (
	"strings"

	"github.com/QuantumNous/new-api/service"
)

const ChannelName = "antigravity"

var ModelList = service.DefaultAntigravityModels()

func alias2ModelName(modelName string) string {
	switch strings.TrimSpace(modelName) {
	case "gemini-2.5-computer-use-preview-10-2025":
		return "rev19-uic3-1p"
	case "gemini-3-pro-image-preview":
		return "gemini-3-pro-image"
	case "gemini-3-pro-preview":
		return "gemini-3-pro-high"
	case "gemini-3-flash-preview":
		return "gemini-3-flash"
	case "gemini-claude-sonnet-4-5":
		return "claude-sonnet-4-5"
	case "gemini-claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-5-thinking"
	case "gemini-claude-opus-4-5-thinking":
		return "claude-opus-4-5-thinking"
	case "gemini-claude-opus-4-6-thinking":
		return "claude-opus-4-6-thinking"
	default:
		return strings.TrimSpace(modelName)
	}
}
