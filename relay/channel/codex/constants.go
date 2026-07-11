package codex

import (
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/samber/lo"
)

var baseModelList = []string{
	"gpt-5", "gpt-5-codex", "gpt-5-codex-mini",
	"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini",
	"gpt-5.2", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.3-codex-spark",
	"gpt-5.4", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
}

var ModelList = append(withCompactModelSuffix(baseModelList), codexImageModel)

const ChannelName = "codex"

const (
	codexImageModel      = "gpt-image-2"
	codexImageOriginator = "codex-tui"
	codexUserAgent       = "codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)"
)

func withCompactModelSuffix(models []string) []string {
	out := make([]string, 0, len(models)*2)
	out = append(out, models...)
	out = append(out, lo.Map(models, func(model string, _ int) string {
		return ratio_setting.WithCompactModelSuffix(model)
	})...)
	return lo.Uniq(out)
}
