package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qqbot/onebot"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const channelNoticeBudget = 30 * time.Second

// NotifyZhipuResetCards announces successful claims, without consuming cards.
func NotifyZhipuResetCards(claims []service.ZhipuResetCardClaim) {
	sendChannelGroupNotice(func(group string) string {
		var lines strings.Builder
		for _, claim := range claims {
			item := claim.Channel
			visible := channel{ID: item.Id, Name: item.Name, Group: item.Group}
			if !visible.inGroup(group) {
				continue
			}
			fmt.Fprintf(&lines, "\n- #%d %s：", visible.ID, visible.displayName())
			if claim.CountsKnown {
				fmt.Fprintf(&lines, "领取到 %d 张 5 小时限额重置卡、%d 张周限额重置卡", claim.FiveHour, claim.Week)
			} else {
				lines.WriteString("领取成功，卡片数量暂时无法确认")
			}
		}
		if lines.Len() == 0 {
			return ""
		}
		return "智谱 Coding Plan 重置卡领取通知：" + lines.String()
	})
}

// sendChannelGroupNotice preserves the configured group filter for each chat.
func sendChannelGroupNotice(noticeForGroup func(string) string) {
	settings := *system_setting.GetQQBotSettings()
	if !settings.Enabled {
		return
	}
	sessions, err := system_setting.ParseQQBotSessions(settings.Sessions)
	if err != nil {
		common.SysError(common.LogText("invalid QQ bot sessions: %s", err.Error()))
		return
	}
	peer := defaultApp().server.Peer()
	if peer == nil {
		common.SysError(common.LogText("failed to send QQ channel notice: bot is not connected to this node"))
		return
	}
	for session, group := range sessions {
		groupID, isGroup := strings.CutPrefix(session, "group:")
		notice := noticeForGroup(group)
		if !isGroup || notice == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), channelNoticeBudget)
		destination := onebot.Event{MessageType: "group", GroupID: json.Number(groupID)}
		err := peer.Send(ctx, destination, []onebot.Segment{onebot.Text(notice)})
		cancel()
		if err != nil {
			common.SysError(common.LogText("failed to send QQ channel notice to %s: %s", session, err.Error()))
		}
	}
}
