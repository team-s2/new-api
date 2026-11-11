package qqbot

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/qqbot/render"
)

const (
	weekSeconds      = 7 * 86400.0
	fiveHourSeconds  = 5 * 3600.0
	monthSeconds     = 30 * 86400.0
	maxTimestamp     = 253402214400.0 // year 9999; the renderer must not see more
	upstreamParallel = 4

	kindCodex      = "codex"
	kindZhipu      = "zhipu"
	kindGrok       = "grok"
	kindOpenCodeGo = "opencode-go"
)

func (c channel) accountKind() string {
	baseURL := ""
	if c.BaseURL != nil {
		baseURL = *c.BaseURL
	}
	switch {
	case c.Type == constant.ChannelTypeCodex:
		return kindCodex
	case constant.IsZhipuCodingPlanChannel(c.Type, baseURL):
		return kindZhipu
	case c.Type == constant.ChannelTypeGrokSub:
		return kindGrok
	case c.Type == constant.ChannelTypeOpenCodeGoSub:
		return kindOpenCodeGo
	}
	return ""
}

// groups returns the channel's comma-separated groups, deduplicated in order.
func (c channel) groups() []string {
	var groups []string
	for part := range strings.SplitSeq(c.Group, ",") {
		if part = strings.TrimSpace(part); part != "" && !slices.Contains(groups, part) {
			groups = append(groups, part)
		}
	}
	return groups
}

// inGroup reports whether the channel belongs to group; no group matches all.
func (c channel) inGroup(group string) bool {
	return group == "" || slices.Contains(c.groups(), group)
}

func (c channel) displayName() string {
	if c.Name == "" {
		return "未命名"
	}
	return c.Name
}

func (c channel) typeName() string {
	if c.accountKind() == kindZhipu {
		return "Zhipu Coding Plan"
	}
	if name, ok := constant.ChannelTypeNames[c.Type]; ok {
		return name
	}
	return fmt.Sprintf("类型 %d", c.Type)
}

// number reads a non-negative finite number from a decoded JSON value.
func number(value any) *float64 {
	var result float64
	switch v := value.(type) {
	case float64:
		result = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil
		}
		result = parsed
	default:
		return nil
	}
	if math.IsNaN(result) || math.IsInf(result, 0) || result < 0 {
		return nil
	}
	return &result
}

func objectField(source map[string]any, key string) map[string]any {
	object, _ := source[key].(map[string]any)
	return object
}

// positive reports a present, non-zero value; upstream sends 0 for "not set".
func positive(value *float64) bool { return value != nil && *value != 0 }

func textField(source map[string]any, key string) string {
	switch value := source[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return ""
}

var (
	status401     = regexp.MustCompile(`\b401\b`)
	status403     = regexp.MustCompile(`\b403\b`)
	upstreamURL   = regexp.MustCompile(`https?://\S+`)
	credentialArg = regexp.MustCompile(`(?i)(bearer\s+|(?:token|password|api_key)[\s:="']+)[^\s,}]+`)
)

// issueText keeps useful provider errors, excluding credentials and HTML.
func issueText(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	lower := strings.ToLower(text)
	switch {
	case status401.MatchString(text):
		return "HTTP 401 · 认证失效，请重新登录或更新渠道凭据"
	case status403.MatchString(text):
		return "HTTP 403 · 上游拒绝访问，请检查账户权限"
	case strings.Contains(text, "OAuth access token expired"):
		return "OAuth 凭据已过期，请重新登录智谱账户"
	case strings.Contains(text, "multi-key"):
		return "暂不支持多密钥渠道的订阅额度查询"
	case strings.Contains(text, "cannot connect"), strings.Contains(lower, "timeout"):
		return "连接失败或请求超时，请稍后重试"
	case strings.Contains(lower, "<html"), strings.Contains(lower, "<!doctype"):
		return "上游返回了网页，无法读取额度数据"
	}
	text = upstreamURL.ReplaceAllString(text, "[上游地址]")
	text = credentialArg.ReplaceAllString(text, "${1}[已隐藏]")
	runes := []rune(text)
	if len(runes) == 0 {
		return "额度读取失败"
	}
	return string(runes[:min(len(runes), 180)])
}

// resetTimestamp accepts provider ISO dates or Unix timestamps.
func resetTimestamp(value any, milliseconds bool) *float64 {
	stamp := number(value)
	if stamp != nil && milliseconds {
		scaled := *stamp / 1000
		stamp = &scaled
	}
	if text, ok := value.(string); ok && stamp == nil {
		if date, err := time.Parse(time.RFC3339, text); err == nil {
			parsed := float64(date.Unix())
			stamp = &parsed
		}
	}
	if stamp == nil || *stamp <= 0 || *stamp >= maxTimestamp {
		return nil
	}
	return stamp
}

// codexWindows picks the five-hour and weekly windows of the main account pool
// from either usage payload layout.
func codexWindows(source map[string]any) (fiveHour, weekly map[string]any) {
	rateLimit := objectField(source, "rate_limit")
	pick := func(key string) map[string]any {
		if window := objectField(rateLimit, key); len(window) > 0 {
			return window
		}
		return objectField(source, key)
	}
	primary, secondary := pick("primary_window"), pick("secondary_window")
	var windows []map[string]any
	for _, window := range []map[string]any{primary, secondary} {
		if window != nil {
			windows = append(windows, window)
		}
	}
	fiveAt, weekAt := -1, -1
	for i, window := range windows {
		seconds := number(window["limit_window_seconds"])
		if seconds == nil {
			continue
		}
		if *seconds >= 86400 && weekAt < 0 {
			weekAt = i
		} else if *seconds < 86400 && fiveAt < 0 {
			fiveAt = i
		}
	}
	plan := textField(source, "plan_type")
	if plan == "" {
		plan = textField(rateLimit, "plan_type")
	}
	if strings.EqualFold(plan, "free") {
		if len(windows) == 0 {
			return nil, nil
		}
		return nil, windows[max(weekAt, 0)]
	}
	if fiveAt < 0 && weekAt < 0 {
		return primary, secondary
	}
	// With one side identified, the other window of a pair fills the gap.
	if len(windows) == 2 {
		if fiveAt < 0 {
			fiveAt = 1 - weekAt
		}
		if weekAt < 0 {
			weekAt = 1 - fiveAt
		}
	}
	if fiveAt >= 0 {
		fiveHour = windows[fiveAt]
	}
	if weekAt >= 0 {
		weekly = windows[weekAt]
	}
	return fiveHour, weekly
}

func parseWindow(raw map[string]any, duration, now float64, zhipu bool) *render.QuotaWindow {
	if len(raw) == 0 {
		return nil
	}
	var used, reset *float64
	if zhipu {
		used = number(raw["percentage"])
		total, current := number(raw["usage"]), number(raw["current_value"])
		if remaining := number(raw["remaining"]); current == nil && total != nil && remaining != nil {
			derived := max(0, *total-*remaining)
			current = &derived
		}
		if positive(total) && current != nil {
			percent := *current / *total * 100
			used = &percent
		}
		if next := number(raw["next_reset_time"]); positive(next) {
			seconds := *next / 1000
			reset = &seconds
		}
	} else {
		used = number(raw["used_percent"])
		if seconds := number(raw["limit_window_seconds"]); positive(seconds) {
			duration = *seconds
		}
		reset = number(raw["reset_at"])
		if !positive(reset) {
			reset = nil
			if after := number(raw["reset_after_seconds"]); after != nil {
				stamp := now + *after
				reset = &stamp
			}
		}
	}
	if reset != nil && (*reset <= 0 || *reset >= maxTimestamp) {
		reset = nil
	}
	if duration > 366*86400 {
		duration = weekSeconds
	}
	return &render.QuotaWindow{UsedPercent: used, Duration: duration, ResetAt: reset}
}

func moneyText(value float64) string {
	places := 2
	if value == math.Trunc(value) {
		places = 0
	}
	whole, fraction, _ := strings.Cut(strconv.FormatFloat(value, 'f', places, 64), ".")
	var b strings.Builder
	b.WriteByte('$')
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	if fraction != "" {
		b.WriteString("." + fraction)
	}
	return b.String()
}

// grokWindow reads a Grok billing window: ISO period bounds and a percentage.
func grokWindow(raw map[string]any, duration float64, percentKey string) *render.QuotaWindow {
	if len(raw) == 0 {
		return nil
	}
	start, reset := resetTimestamp(raw["period_start"], false), resetTimestamp(raw["period_end"], false)
	if start != nil && reset != nil && *reset > *start {
		duration = *reset - *start
	}
	detail := ""
	if used := number(raw["monthly_used"]); used != nil {
		detail = moneyText(*used)
		if limit := number(raw["monthly_limit"]); positive(limit) {
			detail += " / " + moneyText(*limit)
		}
	}
	return &render.QuotaWindow{UsedPercent: number(raw[percentKey]), Duration: duration, ResetAt: reset, Detail: detail}
}

// openCodeGoWindow maps one usage window ({status, percent, resets_at}) onto a
// render window. Percent is already the used fraction.
func openCodeGoWindow(raw map[string]any, duration float64) *render.QuotaWindow {
	if len(raw) == 0 {
		return nil
	}
	used := number(raw["percent"])
	return &render.QuotaWindow{
		UsedPercent: used,
		Duration:    duration,
		ResetAt:     resetTimestamp(raw["resets_at"], false),
	}
}

func normalizeOpenCodeGo(row render.QuotaRow, usage map[string]any, usageErr error) render.QuotaRow {
	if usageErr != nil {
		row.Issue = issueText(usageErr)
		return row
	}
	rolling := objectField(usage, "rolling")
	weekly := objectField(usage, "weekly")
	monthly := objectField(usage, "monthly")
	row.FiveHour = openCodeGoWindow(rolling, fiveHourSeconds)
	row.Weekly = openCodeGoWindow(weekly, weekSeconds)
	row.Monthly = openCodeGoWindow(monthly, monthSeconds)
	for _, window := range []map[string]any{rolling, weekly, monthly} {
		if textField(window, "status") == "rate-limited" {
			row.LimitNote = "上游当前限流"
			break
		}
	}
	if row.FiveHour == nil && row.Weekly == nil && row.Monthly == nil {
		row.Issue = "上游未返回额度窗口"
	}
	return row
}

func normalizeGrok(row render.QuotaRow, usage map[string]any, usageErr error) render.QuotaRow {
	if usageErr != nil {
		row.Issue = issueText(usageErr)
		return row
	}
	row.Plan = textField(usage, "plan")
	row.Weekly = grokWindow(objectField(usage, "weekly"), weekSeconds, "usage_percent")
	row.Monthly = grokWindow(objectField(usage, "monthly"), monthSeconds, "used_percent")
	failed, _ := usage["failed_windows"].([]any)
	var names []string
	if slices.Contains(failed, any("weekly")) {
		names = append(names, "周额度")
	}
	if slices.Contains(failed, any("monthly")) {
		names = append(names, "月额度")
	}
	if len(names) > 0 {
		row.ResetNote = strings.Join(names, "、") + "查询失败"
	}
	if row.Weekly == nil && row.Monthly == nil {
		row.Issue = "上游未返回周额度或月额度"
	}
	return row
}

// zhipuResetPools reads the week and 5h reset cards; the note explains a
// missing section.
func zhipuResetPools(usage map[string]any) ([]render.ResetPool, string) {
	if usage == nil {
		return nil, "上游未提供主动重置次数"
	}
	reset := objectField(usage, "reset")
	if reset == nil {
		reason := strings.TrimSpace(textField(usage, "reset_unavailable_reason"))
		if reason == "" {
			reason = "未知原因"
		}
		return nil, "重置卡查询失败：" + issueText(fmt.Errorf("%s", reason))
	}
	fiveHour, fiveOK := reset["available_five_hour_resets"].([]any)
	week, weekOK := reset["available_week_resets"].([]any)
	if !fiveOK || !weekOK {
		return nil, "上游未提供主动重置次数"
	}
	var pools []render.ResetPool
	for _, section := range []struct {
		label string
		cards []any
	}{{"周重置卡", week}, {"5h 重置卡", fiveHour}} {
		count := len(section.cards)
		pool := render.ResetPool{Label: section.label, Count: &count}
		for _, item := range section.cards {
			if cardData, ok := item.(map[string]any); ok {
				if stamp := resetTimestamp(cardData["expire_at"], true); stamp != nil {
					pool.ExpiresAt = append(pool.ExpiresAt, *stamp)
				}
			}
		}
		slices.Sort(pool.ExpiresAt)
		pools = append(pools, pool)
	}
	return pools, ""
}

// codexResetPool reads the full-reset cards, falling back to the count embedded
// in the usage payload when the dedicated endpoint failed.
func codexResetPool(usage, credits map[string]any, creditsErr error) (render.ResetPool, string) {
	countValue := number(credits["available_count"])
	if countValue == nil {
		countValue = number(objectField(usage, "rate_limit_reset_credits")["available_count"])
	}
	pool := render.ResetPool{Label: "全额重置卡"}
	if countValue != nil {
		count := int(*countValue)
		pool.Count = &count
	}
	cards, _ := credits["credits"].([]any)
	for _, item := range cards {
		cardData, ok := item.(map[string]any)
		if !ok || !strings.EqualFold(textField(cardData, "status"), "available") {
			continue
		}
		if stamp := resetTimestamp(cardData["expires_at"], false); stamp != nil {
			pool.ExpiresAt = append(pool.ExpiresAt, *stamp)
		}
	}
	slices.Sort(pool.ExpiresAt)
	switch {
	case creditsErr != nil && pool.Count != nil:
		return pool, "使用用量接口返回的次数；" + issueText(creditsErr)
	case creditsErr != nil:
		return pool, issueText(creditsErr)
	case pool.Count != nil:
		return pool, ""
	}
	return pool, "未返回重置次数"
}

func normalizeQuota(item channel, usage map[string]any, usageErr error, credits map[string]any, creditsErr error, now float64) render.QuotaRow {
	kind := item.accountKind()
	row := render.QuotaRow{
		ChannelID: item.ID,
		Name:      item.displayName(),
		Status:    render.StatusUnknown,
		Weight:    item.Weight,
		Priority:  item.Priority,
		Groups:    item.groups(),
	}
	switch item.Status {
	case 1:
		row.Status = render.StatusEnabled
	case 2:
		row.Status = render.StatusManualDisabled
	case 3:
		row.Status = render.StatusAutoDisabled
	}
	switch kind {
	case kindCodex:
		row.Provider = "Codex"
		pool, note := codexResetPool(usage, credits, creditsErr)
		row.ResetPools, row.ResetNote = []render.ResetPool{pool}, note
	case kindZhipu:
		row.Provider = "智谱 Coding Plan"
		row.ResetPools, row.ResetNote = zhipuResetPools(usage)
	case kindGrok:
		row.Provider = render.ProviderGrok
		return normalizeGrok(row, usage, usageErr)
	case kindOpenCodeGo:
		row.Provider = "OpenCode Go"
		return normalizeOpenCodeGo(row, usage, usageErr)
	default:
		row.Provider = fmt.Sprintf("类型 %d", item.Type)
		row.Unsupported = true
		row.Issue = "暂不支持此渠道类型的订阅额度查询"
		return row
	}
	if usageErr != nil {
		row.Issue = issueText(usageErr)
		return row
	}
	var fiveRaw, weekRaw map[string]any
	if kind == kindCodex {
		// The main account pool only; additional_rate_limits (including Spark)
		// are deliberately excluded from this subscription overview.
		fiveRaw, weekRaw = codexWindows(usage)
		row.Plan = strings.ToUpper(textField(usage, "plan_type"))
		rateLimit := objectField(usage, "rate_limit")
		if rateLimit["limit_reached"] == true || rateLimit["allowed"] == false {
			row.LimitNote = "上游当前限流"
		}
	} else {
		fiveRaw, weekRaw = objectField(usage, "five_hour"), objectField(usage, "weekly")
		row.Plan = strings.ToUpper(textField(usage, "level"))
	}
	row.Weekly = parseWindow(weekRaw, weekSeconds, now, kind == kindZhipu)
	row.FiveHour = parseWindow(fiveRaw, fiveHourSeconds, now, kind == kindZhipu)
	if row.Weekly == nil && row.FiveHour == nil {
		row.Issue = "上游未返回周额度或 5 小时额度"
	}
	return row
}

// collectQuotas fetches channel quotas with at most four concurrent upstream
// requests. Disabled channels are skipped before any upstream request unless
// includeDisabled is set; a failed channel keeps its row. Rows are in channel
// ID order.
func collectQuotas(ctx context.Context, client *apiClient, includeDisabled bool, group string) ([]render.QuotaRow, error) {
	channels, err := client.allChannels(ctx)
	if err != nil {
		return nil, err
	}
	slots := make(chan struct{}, upstreamParallel)
	fetch := func(id int, suffix string) (map[string]any, error) {
		slots <- struct{}{}
		defer func() { <-slots }()
		return client.usage(ctx, id, suffix)
	}
	var rows []render.QuotaRow
	var mu sync.Mutex
	var fetches sync.WaitGroup
	for _, item := range channels {
		if !item.inGroup(group) || !includeDisabled && item.Status != 1 {
			continue
		}
		fetches.Go(func() {
			var usage, credits map[string]any
			var usageErr, creditsErr error
			switch item.accountKind() {
			case kindCodex:
				var pair sync.WaitGroup
				pair.Go(func() { credits, creditsErr = fetch(item.ID, "codex/usage/reset-credits") })
				usage, usageErr = fetch(item.ID, "codex/usage")
				pair.Wait()
			case kindZhipu:
				usage, usageErr = fetch(item.ID, "zhipu/coding-plan/usage")
			case kindGrok:
				usage, usageErr = fetch(item.ID, "grok/usage")
			case kindOpenCodeGo:
				usage, usageErr = fetch(item.ID, "opencode-go/usage")
			}
			row := normalizeQuota(item, usage, usageErr, credits, creditsErr, float64(time.Now().Unix()))
			mu.Lock()
			rows = append(rows, row)
			mu.Unlock()
		})
	}
	fetches.Wait()
	slices.SortFunc(rows, func(a, b render.QuotaRow) int { return a.ChannelID - b.ChannelID })
	return rows, nil
}
