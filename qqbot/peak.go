package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	peakHoursText  = "周一至周五 14:00-18:00 UTC+8"
	peakSettingKey = "peak_auto_disable"
)

// setPeak turns a channel's peak-hour auto-disable on or off. The peak task
// picks the change up on its next pass and announces any status change.
func (t *commandTurn) setPeak(ctx context.Context, args []string) {
	usage := "用法：" + t.command() + " peak <渠道 ID> on|off"
	if len(args) != 2 || args[1] != "on" && args[1] != "off" {
		t.replyText(usage)
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
	if err != nil || id <= 0 {
		t.replyText(fmt.Sprintf("无效的渠道 ID：%s\n%s", args[0], usage))
		return
	}
	enable := args[1] == "on"

	const action = "修改 new-api 渠道高峰期自动禁用"
	channels, err := t.client.allChannels(ctx)
	if err != nil {
		t.fail(action, err)
		return
	}
	target := t.operableChannel(channels, id, t.group)
	if target == nil {
		return
	}
	if target.accountKind() != kindZhipu {
		t.replyText(fmt.Sprintf("渠道 #%d 不是智谱 Coding Plan 渠道，不支持高峰期自动禁用。", id))
		return
	}

	// Raw values keep every other setting exactly as saved.
	setting := map[string]json.RawMessage{}
	if target.Setting != nil && *target.Setting != "" {
		if err := common.UnmarshalJsonStr(*target.Setting, &setting); err != nil {
			t.fail(action, errors.New("渠道设置不是有效的 JSON"))
			return
		}
	}
	verb := "关闭"
	if enable {
		verb = "开启"
	}
	if enable == (string(setting[peakSettingKey]) == "true") {
		t.replyText(fmt.Sprintf("渠道 #%d 的高峰期自动禁用已经是%s状态，无需修改。", id, verb))
		return
	}
	if enable {
		setting[peakSettingKey] = json.RawMessage("true")
	} else {
		delete(setting, peakSettingKey)
	}
	encoded, err := common.Marshal(setting)
	if err != nil {
		t.fail(action, err)
		return
	}
	if err := t.client.updateChannelSetting(ctx, id, string(encoded)); err != nil {
		t.fail(action, err)
		return
	}
	t.replyText(fmt.Sprintf("已%s渠道 #%d 的高峰期自动禁用（%s）：%s", verb, id, peakHoursText, target.displayName()))
}

// peakNotice is the note for a chat with this group filter; empty when none of
// the changed channels concern it.
func peakNotice(disabled, restored []*model.Channel, group string) string {
	var parts []string
	if lines := channelNoticeLines(disabled, group); lines != "" {
		parts = append(parts, "智谱 Coding Plan 高峰期（"+peakHoursText+"），已自动禁用渠道："+lines)
	}
	if lines := channelNoticeLines(restored, group); lines != "" {
		parts = append(parts, "智谱 Coding Plan 高峰期自动禁用已解除，已恢复渠道："+lines)
	}
	return strings.Join(parts, "\n\n")
}

// NotifyZhipuPeak tells the bound groups which channels the peak task changed.
// The status change stands either way, so a note that cannot be delivered is
// only logged.
func NotifyZhipuPeak(disabled, restored []*model.Channel) {
	sendChannelGroupNotice(func(group string) string { return peakNotice(disabled, restored, group) })
}
