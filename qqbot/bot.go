// Package qqbot is the built-in QQ bot: it accepts a NapCat connection over
// OneBot 11 reverse WebSocket and answers slash commands in bound chats with
// channel management and quota and flow images. Channel commands go through the
// management handlers in process, see newAPIEngine.
package qqbot

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qqbot/onebot"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	maxConcurrent   = 8
	commandDeadline = 5 * time.Minute
)

// app is the process-wide bot. Rendering holds whole images in memory, so each
// image type renders one at a time.
type app struct {
	server     *onebot.Server
	engine     *gin.Engine
	slots      chan struct{}
	quotaImage sync.Mutex
	flowImage  sync.Mutex
}

var defaultApp = sync.OnceValue(func() *app {
	bot := &app{engine: newAPIEngine(), slots: make(chan struct{}, maxConcurrent)}
	bot.server = onebot.New(currentCredentials, bot.handleEvent)
	return bot
})

func currentCredentials() onebot.Credentials {
	settings := system_setting.GetQQBotSettings()
	return onebot.Credentials{Enabled: settings.Enabled, SelfID: settings.SelfID, Secret: settings.Secret}
}

// ServeWS is the OneBot reverse WebSocket endpoint.
func ServeWS(w http.ResponseWriter, r *http.Request) {
	defaultApp().server.ServeHTTP(w, r)
}

// inboundSegment is the part of a received message segment the bot reads.
type inboundSegment struct {
	Type string `json:"type"`
	Data struct {
		Text string `json:"text"`
	} `json:"data"`
}

// commandArgs returns the words after /<prefix>, and whether the message is
// such a command. The slash is the only way to wake the bot.
func commandArgs(event onebot.Event, prefix string) ([]string, bool) {
	var text strings.Builder
	var segments []inboundSegment
	if common.Unmarshal(event.Message, &segments) == nil {
		for _, segment := range segments {
			if segment.Type == "text" {
				text.WriteString(segment.Data.Text)
			}
		}
	} else {
		text.WriteString(event.RawMessage)
	}
	fields := strings.Fields(text.String())
	if len(fields) == 0 || fields[0] != "/"+prefix {
		return nil, false
	}
	return fields[1:], true
}

// sessionID names the chat the way bindings are written in the settings.
func sessionID(event onebot.Event) string {
	if event.MessageType == "group" {
		return "group:" + event.GroupID.String()
	}
	return "private:" + event.UserID.String()
}

func (a *app) handleEvent(event onebot.Event, peer *onebot.Peer) {
	settings := *system_setting.GetQQBotSettings()
	args, ok := commandArgs(event, settings.CommandPrefix)
	if !settings.Enabled || !ok {
		return
	}
	sessions, err := system_setting.ParseQQBotSessions(settings.Sessions)
	if err != nil {
		common.SysError(common.LogText("invalid QQ bot sessions: %s", err.Error()))
		return
	}
	// Unbound chats get no reply at all, so the bot stays invisible there.
	group, bound := sessions[sessionID(event)]
	if !bound {
		return
	}
	turn := &commandTurn{bot: a, event: event, peer: peer, settings: settings, group: group}
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
		operator: "qq:" + event.UserID.String(),
		timeout:  time.Duration(max(settings.RequestTimeout, 1)) * time.Second,
	}
	turn.execute(ctx, args)
}

// commandTurn is one command being answered.
type commandTurn struct {
	bot      *app
	event    onebot.Event
	peer     *onebot.Peer
	settings system_setting.QQBotSettings
	// group is the chat's group filter; empty means every channel.
	group  string
	client *apiClient
}

// command is the slash command as users type it.
func (t *commandTurn) command() string {
	return "/" + t.settings.CommandPrefix
}

func (t *commandTurn) send(message ...onebot.Segment) {
	// The reply gets its own deadline: a command that ran out of time should
	// still be able to say so.
	if err := t.peer.Send(context.Background(), t.event, message); err != nil {
		common.SysError(common.LogText("failed to send QQ reply: %s", err.Error()))
	}
}

func (t *commandTurn) replyText(text string) {
	t.send(onebot.Text(text))
}

// replyImage sends a PNG, after an optional caption, in one message.
func (t *commandTurn) replyImage(caption string, png []byte) {
	image := onebot.Image("base64://" + base64.StdEncoding.EncodeToString(png))
	if caption == "" {
		t.send(image)
		return
	}
	t.send(onebot.Text(caption), image)
}

// fail logs a failed command and reports it to the chat.
func (t *commandTurn) fail(action string, err error) {
	common.SysError(common.LogText("QQ bot command failed: %s: %s", action, err.Error()))
	t.replyText(fmt.Sprintf("%s失败：%v", action, err))
}
