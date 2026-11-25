// Package qqbot is the built-in QQ bot: it connects to the QQ official bot
// gateway and answers @-mentions in bound groups with channel management and
// quota and flow images. Channel commands go through the management handlers in
// process, see newAPIEngine.
package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qqbot/official"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	maxConcurrent = 8
	// commandDeadline stays well inside QQ's one-hour passive reply window,
	// so a command that runs out of time can still say so without hanging in
	// the chat for long.
	commandDeadline = 4 * time.Minute
	replyDeadline   = time.Minute
)

// app is the process-wide bot. Rendering holds whole images in memory, so each
// image type renders one at a time.
type app struct {
	client     *official.Client
	engine     *gin.Engine
	slots      chan struct{}
	quotaImage sync.Mutex
	flowImage  sync.Mutex
}

var defaultApp = sync.OnceValue(func() *app {
	bot := &app{engine: newAPIEngine(), slots: make(chan struct{}, maxConcurrent)}
	bot.client = official.New(currentCredentials, bot.handleMessage)
	return bot
})

func currentCredentials() official.Credentials {
	settings := system_setting.GetQQBotSettings()
	return official.Credentials{Enabled: settings.Enabled, AppID: settings.AppID, AppSecret: settings.AppSecret}
}

// Run keeps the bot connected to the QQ gateway until ctx ends. QQ allows one
// gateway session per bot, so only the master node runs it.
func Run(ctx context.Context) {
	defaultApp().client.Run(ctx)
}

// commandArgs splits a message into the subcommand and its arguments. The
// transport has already removed the @-mention; the slash the QQ client adds to
// a registered command is optional.
func commandArgs(text string) []string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	if fields[0] = strings.TrimPrefix(fields[0], "/"); fields[0] == "" {
		return fields[1:]
	}
	return fields
}

func (a *app) handleMessage(m *official.Message) {
	settings := *system_setting.GetQQBotSettings()
	if !settings.Enabled {
		return
	}
	args := commandArgs(m.Text)
	// The binding line is written the way the settings take it.
	session := "group:" + m.Group
	turn := &commandTurn{bot: a, message: m, settings: settings}
	// QQ identifies groups by per-bot openids rather than group numbers, so any
	// group may ask for its binding line.
	if len(args) == 1 && strings.EqualFold(args[0], "id") {
		turn.replyText("当前会话：" + session + "\n管理员将这一行加入 QQ 机器人设置的「绑定的会话」后即可使用命令。")
		return
	}
	sessions, err := system_setting.ParseQQBotSessions(settings.Sessions)
	if err != nil {
		common.SysError(common.LogText("invalid QQ bot sessions: %s", err.Error()))
		return
	}
	// Unbound groups get no other reply, so the bot stays quiet there.
	group, bound := sessions[session]
	if !bound {
		return
	}
	turn.group = group
	select {
	case a.slots <- struct{}{}:
	default:
		turn.replyText("当前操作较多，请稍后重试。")
		return
	}
	defer func() {
		<-a.slots
		if failure := recover(); failure != nil {
			common.SysError(common.LogText("QQ bot command panicked: %v", failure))
			turn.replyText("操作失败，请稍后重试。")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), commandDeadline)
	defer cancel()
	turn.client = &apiClient{
		engine:   a.engine,
		operator: "qq:" + m.User,
		timeout:  time.Duration(max(settings.RequestTimeout, 1)) * time.Second,
	}
	turn.execute(ctx, args)
}

// commandTurn is one command being answered.
type commandTurn struct {
	bot      *app
	message  *official.Message
	settings system_setting.QQBotSettings
	// group is the chat's group filter; empty means every channel.
	group  string
	client *apiClient
}

// command shows how to run a subcommand: the bot only wakes on an @-mention.
func (t *commandTurn) command(subcommand string) string {
	name := t.bot.client.Name()
	if name == "" {
		name = "机器人"
	}
	return "@" + name + " " + subcommand
}

// replyText and replyImage get their own deadline: a command that ran out of
// time should still be able to say so.
func (t *commandTurn) replyText(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), replyDeadline)
	defer cancel()
	if err := t.bot.client.Reply(ctx, t.message, text); err != nil {
		common.SysError(common.LogText("failed to send QQ reply: %s", err.Error()))
	}
}

// replyImage sends a PNG, after an optional caption, in one message.
func (t *commandTurn) replyImage(caption string, png []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), replyDeadline)
	defer cancel()
	if err := t.bot.client.ReplyImage(ctx, t.message, caption, png); err != nil {
		common.SysError(common.LogText("failed to send QQ image reply: %s", err.Error()))
	}
}

// fail logs a failed command and reports it to the chat.
func (t *commandTurn) fail(action string, err error) {
	common.SysError(common.LogText("QQ bot command failed: %s: %s", action, err.Error()))
	t.replyText(fmt.Sprintf("%s失败：%v", action, err))
}
