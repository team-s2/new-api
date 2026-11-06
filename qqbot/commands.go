package qqbot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/qqbot/render"
)

const (
	channelEnabled        = 1
	channelManualDisabled = 2
	channelAutoDisabled   = 3
	allGroupArg           = "allgroup"
	allChannelsArg        = "all"
	maxFlowTopN           = 100
)

var helpEntries = [][2]string{
	{"quota [all]", "生成额度图（含权重与优先级，按渠道编号排序）；加 all 时包含已禁用渠道"},
	{"on <渠道 ID>", "启用渠道并返回最新额度图"},
	{"off <渠道 ID>", "禁用渠道并返回最新额度图"},
	{"weight <渠道 ID|all> <数值>", "设置渠道权重（越大分到的请求越多）并返回最新额度图；all 修改当前群聊可操作的全部渠道"},
	{"priority <渠道 ID|all> <数值>", "设置渠道优先级（越大越优先选中）并返回最新额度图；all 修改当前群聊可操作的全部渠道"},
	{"peak <渠道 ID> on|off", "开启或关闭智谱 Coding Plan 渠道的高峰期自动禁用（" + peakHoursText + "）"},
	{"flow [时间范围]", "生成 Dashboard Flow 流图；时间范围支持 30m、1h、7d 等格式"},
}

func (t *commandTurn) execute(ctx context.Context, args []string) {
	if len(args) == 0 {
		t.help()
		return
	}
	switch rest := args[1:]; args[0] {
	case "quota":
		t.quota(ctx, rest)
	case "flow":
		t.flow(ctx, rest)
	case "on":
		t.setStatus(ctx, rest, true)
	case "off":
		t.setStatus(ctx, rest, false)
	case "peak":
		t.setPeak(ctx, rest)
	case "weight":
		t.setRouting(ctx, rest, weightField)
	case "priority":
		t.setRouting(ctx, rest, priorityField)
	default:
		t.help()
	}
}

func (t *commandTurn) help() {
	var b strings.Builder
	b.WriteString(t.command() + " 子命令：\n\n")
	for _, entry := range helpEntries {
		fmt.Fprintf(&b, "- %s %s：%s\n", t.command(), entry[0], entry[1])
	}
	b.WriteString("\n仅绑定了 new-api 的会话可以执行以上命令。")
	t.replyText(b.String())
}

// withoutAllGroup removes the allgroup flag from args.
func withoutAllGroup(args []string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.EqualFold(arg, allGroupArg) {
			kept = append(kept, arg)
		}
	}
	return kept, len(kept) != len(args)
}

func (t *commandTurn) renderQuota(rows []render.QuotaRow) ([]byte, error) {
	t.bot.quotaImage.Lock()
	defer t.bot.quotaImage.Unlock()
	return render.Quota(rows, float64(time.Now().Unix()), t.settings.FontPath, t.settings.HiddenGroupSet())
}

func (t *commandTurn) renderFlow(rows []render.FlowRow) ([]byte, error) {
	t.bot.flowImage.Lock()
	defer t.bot.flowImage.Unlock()
	return render.Flow(rows, render.FlowOptions{
		Stages:   t.settings.OrderedFlowStages(),
		TopLimit: max(1, min(t.settings.FlowTopN, maxFlowTopN)),
		Overflow: t.settings.FlowOverflow,
		FontPath: t.settings.FontPath,
	})
}

func (t *commandTurn) quota(ctx context.Context, args []string) {
	scope, allGroup := withoutAllGroup(args)
	includeDisabled := len(scope) == 1 && strings.EqualFold(scope[0], allChannelsArg)
	if len(scope) > 0 && !includeDisabled {
		t.replyText("用法：" + t.command() + " quota [all]")
		return
	}
	group := t.group
	if allGroup {
		group = ""
	}
	const action = "生成 new-api 额度图"
	rows, err := collectQuotas(ctx, t.client, includeDisabled, group)
	if err != nil {
		t.fail(action, err)
		return
	}
	png, err := t.renderQuota(rows)
	if err != nil {
		t.fail(action, err)
		return
	}
	t.replyImage("", png)
}

var flowDuration = regexp.MustCompile(`(?i)^([1-9][0-9]*)([mhd])$`)

// parseFlowDuration converts a compact duration such as 30m, 1h or 7d to seconds.
func parseFlowDuration(value string) (int64, error) {
	invalid := errors.New("时间范围格式错误，请使用 30m、1h 或 7d 等格式")
	match := flowDuration.FindStringSubmatch(value)
	if match == nil {
		return 0, invalid
	}
	unit := map[string]int64{"m": 60, "h": 3600, "d": 86400}[strings.ToLower(match[2])]
	count, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || count > math.MaxInt64/unit {
		return 0, invalid
	}
	return count * unit, nil
}

func (t *commandTurn) flow(ctx context.Context, args []string) {
	duration, allGroup := withoutAllGroup(args)
	group := t.group
	if allGroup {
		group = ""
	}
	const action = "生成 new-api 流图"
	rangeSeconds := max(3600, int64(t.settings.FlowHours)*3600)
	if len(duration) > 0 {
		parsed, err := parseFlowDuration(strings.Join(duration, " "))
		if err != nil {
			t.fail(action, err)
			return
		}
		rangeSeconds = parsed
	}
	end := time.Now().Unix()
	rows, err := t.client.flow(ctx, end-rangeSeconds, end)
	if err != nil {
		t.fail(action, err)
		return
	}
	// Channel tests are logged without a token, so token_id is 0.
	rows = slices.DeleteFunc(rows, func(row render.FlowRow) bool { return row.TokenID == 0 })
	if group != "" {
		channels, err := t.client.allChannels(ctx)
		if err != nil {
			t.fail(action, err)
			return
		}
		visible := map[int]bool{}
		for _, item := range channels {
			visible[item.ID] = item.inGroup(group)
		}
		rows = slices.DeleteFunc(rows, func(row render.FlowRow) bool { return !visible[row.ChannelID] })
	}
	if len(rows) == 0 {
		t.fail(action, errors.New("所选时间范围内没有流图数据"))
		return
	}
	png, err := t.renderFlow(rows)
	if err != nil {
		t.fail(action, err)
		return
	}
	t.replyImage("", png)
}

func findChannel(channels []channel, id int) *channel {
	for i := range channels {
		if channels[i].ID == id {
			return &channels[i]
		}
	}
	return nil
}

// operableChannel finds a channel the chat may change, telling the sender why
// when there is none.
func (t *commandTurn) operableChannel(channels []channel, id int, group string) *channel {
	target := findChannel(channels, id)
	if target == nil {
		t.replyText(fmt.Sprintf("渠道 #%d 不存在。", id))
		return nil
	}
	if !target.inGroup(group) {
		t.replyText(fmt.Sprintf("渠道 #%d 不在当前群聊可操作的分组内。", id))
		return nil
	}
	return target
}

// channelSummary names a changed channel by its name and plan.
func channelSummary(target *channel, rows []render.QuotaRow) string {
	version := target.typeName()
	index := slices.IndexFunc(rows, func(row render.QuotaRow) bool { return row.ChannelID == target.ID })
	if index >= 0 {
		version = rows[index].VersionLabel()
	}
	return fmt.Sprintf("%s (%s)", target.displayName(), version)
}

func channelStatusText(status int) string {
	switch status {
	case channelEnabled:
		return "启用"
	case channelManualDisabled:
		return "手动禁用"
	case channelAutoDisabled:
		return "自动禁用"
	}
	return "未知"
}

func (t *commandTurn) setStatus(ctx context.Context, args []string, enable bool) {
	group := t.group
	id := 0
	if len(args) == 1 {
		id, _ = strconv.Atoi(args[0])
	}
	if id <= 0 {
		t.replyText("用法：" + t.command() + " on|off <渠道 ID>")
		return
	}
	const action = "修改 new-api 渠道状态"
	channels, err := t.client.allChannels(ctx)
	if err != nil {
		t.fail(action, err)
		return
	}
	target := t.operableChannel(channels, id, group)
	if target == nil {
		return
	}
	verb, status := "禁用", channelManualDisabled
	if enable {
		verb, status = "启用", channelEnabled
	}
	alreadyDisabled := target.Status == channelManualDisabled || target.Status == channelAutoDisabled
	if enable && target.Status == channelEnabled || !enable && alreadyDisabled {
		t.replyText(fmt.Sprintf("渠道 #%d 已经是%s状态，无需%s。", id, channelStatusText(target.Status), verb))
		return
	}
	changed, err := t.client.updateChannelStatus(ctx, id, status)
	if err != nil {
		t.fail(action, err)
		return
	}
	if !changed {
		t.replyText(fmt.Sprintf("渠道 #%d 状态未发生变化，请重试。", id))
		return
	}
	rows, err := collectQuotas(ctx, t.client, true, group)
	if err != nil {
		t.fail(action, err)
		return
	}
	png, err := t.renderQuota(rows)
	if err != nil {
		t.fail(action, err)
		return
	}
	t.replyImage(fmt.Sprintf("已%s渠道 #%d：%s", verb, id, channelSummary(target, rows)), png)
}

// routingField describes a settable routing knob. new-api stores weight as an
// unsigned int and priority as a signed 64-bit int; int32 bounds keep the
// values safely round-trippable through every client.
type routingField struct {
	name, label, meaning string
	low, high            int64
	current              func(channel) *int64
}

var (
	weightField = routingField{
		name: "weight", label: "权重", meaning: "同优先级内越大分到的请求越多",
		low: 0, high: math.MaxInt32,
		current: func(c channel) *int64 { return c.Weight },
	}
	priorityField = routingField{
		name: "priority", label: "优先级", meaning: "越大越优先选中",
		low: math.MinInt32, high: math.MaxInt32,
		current: func(c channel) *int64 { return c.Priority },
	}
)

// routingTarget is the channel a weight or priority command names; zero means
// every operable channel.
func routingTarget(raw string) (int, bool) {
	if strings.EqualFold(raw, allChannelsArg) {
		return 0, true
	}
	id, err := strconv.Atoi(strings.TrimPrefix(raw, "#"))
	return id, err == nil && id > 0
}

// routingFailure is a channel an all-channels update could not change.
type routingFailure struct {
	channel channel
	err     error
}

// updateRouting writes the value on every channel, four at a time.
func (t *commandTurn) updateRouting(ctx context.Context, channels []channel, field routingField, value int64) []routingFailure {
	var failures []routingFailure
	var mu sync.Mutex
	var updates sync.WaitGroup
	slots := make(chan struct{}, upstreamParallel)
	for _, item := range channels {
		updates.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := t.client.updateChannelField(ctx, item.ID, field.name, value); err != nil {
				mu.Lock()
				failures = append(failures, routingFailure{item, err})
				mu.Unlock()
			}
		})
	}
	updates.Wait()
	slices.SortFunc(failures, func(a, b routingFailure) int { return a.channel.ID - b.channel.ID })
	return failures
}

func (t *commandTurn) setRouting(ctx context.Context, args []string, field routingField) {
	usage := fmt.Sprintf("用法：%[1]s %[2]s <渠道 ID|all> <%[3]s>（%[4]s）\nall 会修改当前群聊可操作的全部渠道，例如 %[1]s %[2]s all 1",
		t.command(), field.name, field.label, field.meaning)
	group := t.group
	if len(args) != 2 {
		t.replyText(usage)
		return
	}
	id, ok := routingTarget(args[0])
	if !ok {
		t.replyText(fmt.Sprintf("无效的渠道 ID：%s\n%s", args[0], usage))
		return
	}
	value, err := strconv.ParseInt(args[1], 10, 64)
	if errors.Is(err, strconv.ErrSyntax) {
		t.replyText(fmt.Sprintf("%s必须是整数：%s", field.label, args[1]))
		return
	}
	if err != nil || value < field.low || value > field.high {
		t.replyText(fmt.Sprintf("%s必须在 %d 到 %d 之间。", field.label, field.low, field.high))
		return
	}

	action := "修改 new-api 渠道" + field.label
	channels, err := t.client.allChannels(ctx)
	if err != nil {
		t.fail(action, err)
		return
	}
	var selected []channel
	if id == 0 {
		for _, item := range channels {
			if item.inGroup(group) {
				selected = append(selected, item)
			}
		}
		if len(selected) == 0 {
			scope := ""
			if group != "" {
				scope = "分组 " + group + " "
			}
			t.replyText("当前群聊" + scope + "没有可操作的渠道。")
			return
		}
	} else {
		target := t.operableChannel(channels, id, group)
		if target == nil {
			return
		}
		selected = []channel{*target}
	}

	pending := slices.DeleteFunc(slices.Clone(selected), func(item channel) bool {
		current := field.current(item)
		return current != nil && *current == value
	})
	if len(pending) == 0 {
		subject := fmt.Sprintf("全部 %d 个渠道的%s", len(selected), field.label)
		if id != 0 {
			subject = fmt.Sprintf("渠道 #%d 的%s", id, field.label)
		}
		t.replyText(fmt.Sprintf("%s已经是 %d，无需修改。", subject, value))
		return
	}

	failures := t.updateRouting(ctx, pending, field, value)
	if id != 0 && len(failures) > 0 {
		t.fail(action, failures[0].err)
		return
	}
	rows, err := collectQuotas(ctx, t.client, true, group)
	if err != nil {
		t.fail(action, err)
		return
	}
	var details strings.Builder
	if id != 0 {
		fmt.Fprintf(&details, "已将渠道 #%d 的%s设为 %d：%s", id, field.label, value, channelSummary(&selected[0], rows))
	} else {
		if group != "" {
			fmt.Fprintf(&details, "分组 %s：", group)
		}
		fmt.Fprintf(&details, "已将 %d 个渠道的%s设为 %d", len(pending)-len(failures), field.label, value)
		if len(selected) > len(pending) {
			fmt.Fprintf(&details, "，%d 个原本已是 %d", len(selected)-len(pending), value)
		}
		if len(failures) > 0 {
			fmt.Fprintf(&details, "\n%d 个渠道修改失败：", len(failures))
			for _, failure := range failures {
				fmt.Fprintf(&details, "\n- #%d %s：%v", failure.channel.ID, failure.channel.displayName(), failure.err)
			}
		}
	}
	png, err := t.renderQuota(rows)
	if err != nil {
		t.fail(action, err)
		return
	}
	t.replyImage(details.String(), png)
}
