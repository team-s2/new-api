package system_setting

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

const (
	// QQBotOptionPrefix starts every persisted QQ bot option key.
	QQBotOptionPrefix = "qq_bot."
)

// QQBotFlowStageOrder is the order flow stages are always drawn in.
var QQBotFlowStageOrder = []string{"user", "node", "token", "group", "model", "channel"}

// QQBotSettings configures the built-in QQ official bot. The app_secret key
// ends in "secret" so GetOptions never returns it to the web console.
type QQBotSettings struct {
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	// UserID is the new-api user that channel changes are audited under.
	UserID int `json:"user_id"`
	// Sessions binds QQ groups to the bot, one per line: group:<group openid>,
	// optionally followed by =<new-api group> to limit the chat to that group.
	// The bot's id command prints a group's line.
	Sessions string `json:"sessions"`
	// HiddenGroups lists groups the quota image shows no tag for.
	HiddenGroups   string `json:"hidden_groups"`
	FlowStages     string `json:"flow_stages"`
	FlowHours      int    `json:"flow_hours"`
	FlowTopN       int    `json:"flow_top_n"`
	FlowOverflow   string `json:"flow_overflow"`
	RequestTimeout int    `json:"request_timeout"`
	FontPath       string `json:"font_path"`
}

var qqBotSettings = QQBotSettings{
	UserID:         1,
	FlowStages:     "user,model,channel",
	FlowHours:      24,
	FlowTopN:       20,
	FlowOverflow:   "aggregate",
	RequestTimeout: 20,
}

func init() {
	config.GlobalConfig.Register("qq_bot", &qqBotSettings)
}

func GetQQBotSettings() *QQBotSettings {
	return &qqBotSettings
}

// listItems splits a comma or line separated option into trimmed entries.
func listItems(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r'
	})
}

// HiddenGroupSet returns the groups the quota image leaves untagged.
func (s *QQBotSettings) HiddenGroupSet() map[string]bool {
	hidden := map[string]bool{}
	for _, group := range listItems(s.HiddenGroups) {
		if group = strings.TrimSpace(group); group != "" {
			hidden[group] = true
		}
	}
	return hidden
}

// OrderedFlowStages returns the selected stages in drawing order.
func (s *QQBotSettings) OrderedFlowStages() []string {
	selected := listItems(s.FlowStages)
	for i := range selected {
		selected[i] = strings.TrimSpace(selected[i])
	}
	stages := make([]string, 0, len(QQBotFlowStageOrder))
	for _, stage := range QQBotFlowStageOrder {
		if slices.Contains(selected, stage) {
			stages = append(stages, stage)
		}
	}
	return stages
}

var (
	// QQ openids are opaque; the pattern only keeps a line from carrying
	// separators or path characters into an OpenAPI URL.
	qqBotSessionPattern = regexp.MustCompile(`^group:[0-9A-Za-z_-]{1,128}$`)
	qqBotAppIDPattern   = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// ParseQQBotSessions maps each bound session to its group filter, which is
// empty for a chat that sees every channel.
func ParseQQBotSessions(raw string) (map[string]string, error) {
	sessions := map[string]string{}
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		session, group, filtered := strings.Cut(line, "=")
		session, group = strings.TrimSpace(session), strings.TrimSpace(group)
		if !qqBotSessionPattern.MatchString(session) || filtered && group == "" {
			return nil, common.NewMessage("Invalid QQ session: {{session}}", map[string]any{"session": line})
		}
		if existing, ok := sessions[session]; ok && existing != group {
			return nil, common.NewMessage("QQ session {{session}} has conflicting group filters", map[string]any{"session": session})
		}
		sessions[session] = group
	}
	return sessions, nil
}

// ValidateQQBotOption rejects a settings change the bot could not run with.
// Enabling is checked against the values already saved, so the web console
// saves qq_bot.enabled last.
func ValidateQQBotOption(key, value string) error {
	switch strings.TrimPrefix(key, QQBotOptionPrefix) {
	case "enabled":
		if value == "true" && (qqBotSettings.AppID == "" || qqBotSettings.AppSecret == "") {
			return common.NewMessage("Cannot enable the QQ bot. Enter the AppID and AppSecret first.")
		}
	case "app_id":
		if !qqBotAppIDPattern.MatchString(value) {
			return common.NewMessage("The AppID must be a positive integer")
		}
	case "app_secret":
		if value == "" || strings.ContainsFunc(value, unicode.IsSpace) {
			return common.NewMessage("The AppSecret must not be empty or contain spaces")
		}
	case "sessions":
		_, err := ParseQQBotSessions(value)
		return err
	case "flow_stages":
		candidate := QQBotSettings{FlowStages: value}
		if len(candidate.OrderedFlowStages()) < 2 {
			return common.NewMessage("Select at least two flow stages")
		}
	case "flow_overflow":
		if value != "aggregate" && value != "hide" {
			return common.NewMessage("Invalid parameters")
		}
	case "user_id", "flow_hours", "flow_top_n", "request_timeout":
		if number, err := strconv.Atoi(value); err != nil || number <= 0 {
			return common.NewMessage("Invalid parameters")
		}
	}
	return nil
}
