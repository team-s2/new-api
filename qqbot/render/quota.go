package render

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The quota overview has one section per channel, always in channel ID order. The
// weekly window sits on a date axis and the 5h window on an hour axis, each
// drawn where it falls in time with dark = used and light = remaining; an
// orange line marks now, and used quota past it is ahead of pace and turns red.
// Reset cards get a third, narrow date axis at the end of the row, one thin
// lane per card type; Grok, which has none, shows its monthly dollar allowance
// there instead. Model-group quotas use a two-column grid inside the channel row,
// without inventing shared time windows. Failed channels keep their row: their
// windows are blanked and the error is printed across the row.

const (
	dayLength  = 86400.0
	hourLength = 3600.0

	panel    = Color(0xFFFFFFFF)
	rule     = Color(0xFFEDEFF3)
	grid     = Color(0xFFF1F2F5)
	sub      = Color(0xFF6B7280)
	muted    = Color(0xFFA3A9B5)
	today    = Color(0xFFF6F7FB)
	nowColor = Color(0xFFF08A24)
	over     = Color(0xFFE5484D)
	warn     = Color(0xFFEC8E2C)
	week     = Color(0xFF6B5FD3)
	weekPale = Color(0xFFE7E4F8)
	five     = Color(0xFF12998F)
	fivePale = Color(0xFFDDF1EE)
	card     = Color(0xFF5B5BD6)
	cardLane = Color(0xFFE1E1F7)
	enabled  = Color(0xFF2F9E6E)
	pageBG   = Color(0xFFF4F5F7)

	quotaWidth = 1292
	margin     = 24.0
	pad        = 20.0
	left       = margin + pad
	right      = quotaWidth - margin - pad
	nameWidth  = 192.0
	// Right edges of the value cells, then the three time axes they introduce.
	weightValue        = 272.0
	priorityValue      = 304.0
	weekValue          = 370.0
	weekX0             = 384.0
	weekX1             = 702.0
	fiveValue          = 770.0
	fiveX0             = 784.0
	fiveX1             = 982.0
	cardLabel          = 996.0
	cardValue          = 1028.0
	cardX0             = 1040.0
	titleHeight        = 60.0
	headHeight         = 34.0
	rowHeight          = 52.0
	laneOffset         = 9.0 // Zhipu's week and 5h card lanes sit this far above/below centre
	chipGap            = 10.0
	barHeight          = 12.0
	tagHeight          = 15.0
	tagPad             = 4.0
	tagGap             = 4.0
	maxQuotaImage      = 20000.0
	quotaBucketColumns = 2
)

// tagColors are (fill, text) pairs; a group keeps its colour across rows.
var tagColors = [][2]Color{
	{0xFFE7E4F8, 0xFF4F43B8},
	{0xFFDDF1EE, 0xFF0B7A72},
	{0xFFFCEBDD, 0xFFB15E12},
	{0xFFE3EEFB, 0xFF2463AE},
	{0xFFF8E3EC, 0xFFB0336A},
	{0xFFEBEDF1, 0xFF4B5262},
}

// imageZone is UTC+8, the time zone of every time drawn in the images.
var imageZone = time.FixedZone("UTC+8", 8*3600)

// Status labels of a channel row.
const (
	StatusEnabled        = "启用"
	StatusManualDisabled = "手动禁用"
	StatusAutoDisabled   = "自动禁用"
	StatusUnknown        = "状态未知"
)

// ProviderGrok names the provider whose row shows a monthly allowance.
const ProviderGrok = "Grok"

// QuotaWindow is one subscription window; times are Unix seconds.
type QuotaWindow struct {
	UsedPercent *float64
	Duration    float64
	ResetAt     *float64
	Detail      string
}

func (w *QuotaWindow) startAt() float64 { return *w.ResetAt - w.Duration }

// ResetPool is one kind of reset card.
type ResetPool struct {
	Label     string
	Count     *int
	ExpiresAt []float64
}

// QuotaBucket is an independently reported model-group quota window. Missing
// percentages and reset times stay unknown rather than implying a full balance.
type QuotaBucket struct {
	ID               string
	Name             string
	RemainingPercent *float64
	ResetAt          *float64
	Disabled         bool
}

// QuotaRow is one channel section of the overview.
type QuotaRow struct {
	ChannelID    int
	Name         string
	Provider     string
	Status       string
	Weight       *int64
	Priority     *int64
	Plan         string
	Weekly       *QuotaWindow
	FiveHour     *QuotaWindow
	Monthly      *QuotaWindow
	QuotaBuckets []QuotaBucket
	ResetNote    string
	ResetPools   []ResetPool
	Issue        string
	Unsupported  bool
	LimitNote    string
	Groups       []string
}

func (r QuotaRow) height() float64 {
	if r.Issue != "" {
		return rowHeight
	}
	return float64(max(1, (len(r.QuotaBuckets)+quotaBucketColumns-1)/quotaBucketColumns)) * rowHeight
}

// VersionLabel returns the provider and plan label shown under a channel name.
func (r QuotaRow) VersionLabel() string {
	provider := r.Provider
	if strings.HasPrefix(provider, "智谱") {
		provider = "智谱"
	}
	if strings.Contains(r.Plan, provider) {
		return r.Plan
	}
	return strings.TrimSpace(provider + " " + r.Plan)
}

func dateText(timestamp float64, layout string) string {
	return time.Unix(int64(timestamp), 0).In(imageZone).Format(layout)
}

func dayStart(timestamp float64) float64 {
	const offset = 8 * hourLength
	return math.Floor((timestamp+offset)/dayLength)*dayLength - offset
}

// durationText gives the two most significant units, e.g. 4天21时, with narrow
// gaps around the units.
func durationText(seconds float64) string {
	minutes := max(1, int(seconds/60))
	days, hours, mins := minutes/1440, minutes%1440/60, minutes%60
	type part struct {
		value int
		unit  string
	}
	var parts []part
	switch {
	case days > 0:
		parts = []part{{days, "天"}, {hours, "时"}}
	case hours > 0:
		parts = []part{{hours, "时"}, {mins, "分"}}
	default:
		parts = []part{{mins, "分"}}
	}
	if len(parts) == 2 && parts[1].value == 0 {
		parts = parts[:1]
	}
	pieces := make([]string, 0, len(parts))
	for _, item := range parts {
		pieces = append(pieces, strconv.Itoa(item.value)+unitGap+item.unit)
	}
	return strings.Join(pieces, unitGap)
}

// percentText shows whole percents from 10 up, one decimal below.
func percentText(value float64) string {
	if value >= 10 || value == 0 {
		return strconv.FormatFloat(value, 'f', 0, 64) + "%"
	}
	text := strconv.FormatFloat(value, 'f', 1, 64)
	return strings.TrimRight(strings.TrimRight(text, "0"), ".") + "%"
}

// axis is a linear time scale from start to end over pixels x0..x1.
type axis struct {
	start, end, x0, x1 float64
}

func (a axis) x(stamp float64) float64 {
	return a.x0 + (stamp-a.start)/(a.end-a.start)*(a.x1-a.x0)
}

func (a axis) clamp(x float64) float64 { return min(a.x1, max(a.x0, x)) }

func (a axis) days() int { return int(math.Round((a.end - a.start) / dayLength)) }

// weekAxis covers, in whole local days, every live weekly window and today.
func weekAxis(rows []QuotaRow, now float64) axis {
	first, last := now, now
	for i := range rows {
		window := rows[i].Weekly
		if window != nil && window.ResetAt != nil && *window.ResetAt > now {
			first, last = min(first, window.startAt()), max(last, *window.ResetAt)
		}
	}
	start := dayStart(first)
	return axis{start, max(dayStart(last)+dayLength, start+8*dayLength), weekX0, weekX1}
}

// cardAxis covers, in whole local days, today (or the oldest listed card) to
// the last card.
func cardAxis(rows []QuotaRow, now float64) axis {
	first, last := now, now
	for i := range rows {
		for _, pool := range rows[i].ResetPools {
			for _, stamp := range pool.ExpiresAt {
				first, last = min(first, stamp), max(last, stamp)
			}
		}
	}
	start := dayStart(first)
	return axis{start, max(dayStart(last)+dayLength, start+14*dayLength), cardX0, right}
}

func tagColor(group string) [2]Color {
	sum := 0
	for _, b := range []byte(group) {
		sum += int(b)
	}
	return tagColors[sum%len(tagColors)]
}

// drawGroupTags places pills from x rightwards; groups that do not fit
// collapse into +n.
func drawGroupTags(p painter, x, cy float64, groups []string, limit float64) {
	widths := make([]float64, len(groups))
	total := 0.0
	for i, group := range groups {
		widths[i] = p.measure(group, 10.5, Bold) + 2*tagPad
		total += widths[i]
	}
	shown := len(groups)
	for shown > 0 && x+total+tagGap*float64(shown-1) > limit {
		shown--
		total -= widths[shown]
		more := p.measure(fmt.Sprintf("+%d", len(groups)-shown), 10.5, Bold) + 2*tagPad
		if x+total+tagGap*float64(shown)+more <= limit {
			break
		}
	}
	for i, group := range groups[:shown] {
		colors := tagColor(group)
		p.rect(x, cy-tagHeight/2, x+widths[i], cy+tagHeight/2, colors[0], 4)
		p.text(x+widths[i]/2, cy, group, 10.5, colors[1], Bold, alignCenter)
		x += widths[i] + tagGap
	}
	if shown < len(groups) {
		label := fmt.Sprintf("+%d", len(groups)-shown)
		width := p.measure(label, 10.5, Bold) + 2*tagPad
		p.rect(x, cy-tagHeight/2, x+width, cy+tagHeight/2, grid, 4)
		p.text(x+width/2, cy, label, 10.5, sub, Bold, alignCenter)
	}
}

func drawName(p painter, cy float64, row *QuotaRow, hidden map[string]bool) {
	isEnabled := row.Status == StatusEnabled
	dot, nameColor := muted, sub
	if isEnabled {
		dot, nameColor = enabled, ink
	}
	p.circle(left+4, cy, 4, dot)
	tag := 0.0
	if row.LimitNote != "" {
		tag = p.measure("限流", 11, Bold) + 12
	}
	name := p.fit(row.Name, nameWidth-22-tag, 15, Bold)
	width := p.text(left+16, cy, name, 15, nameColor, Bold, alignLeft)
	if tag > 0 {
		x := left + 22 + width
		p.rect(x, cy-9, x+tag, cy+9, over, 9)
		p.text(x+tag/2, cy, "限流", 11, panel, Bold, alignCenter)
	}
	parts := []string{fmt.Sprintf("#%d", row.ChannelID), row.VersionLabel()}
	// The grey dot already says "disabled"; only the less common reasons
	// (auto-disabled, unknown status) are spelled out.
	if !isEnabled && row.Status != StatusManualDisabled {
		parts = append(parts, row.Status)
	}
	meta := p.fit(strings.Join(parts, " · "), nameWidth-16, 12, Regular)
	width = p.text(left+16, cy+19, meta, 12, sub, Regular, alignLeft)
	groups := make([]string, 0, len(row.Groups))
	for _, group := range row.Groups {
		if !hidden[group] {
			groups = append(groups, group)
		}
	}
	if len(groups) > 0 {
		drawGroupTags(p, left+22+width, cy+19, groups, left+nameWidth)
	}
}

func drawWindowBar(p painter, window *QuotaWindow, cy float64, a axis, now float64, usedColor, pale Color) {
	if window == nil || window.ResetAt == nil || *window.ResetAt <= now {
		return
	}
	raw0, raw1 := a.x(window.startAt()), a.x(*window.ResetAt)
	x0, x1 := a.clamp(raw0), a.clamp(raw1)
	if x1-x0 < 1 {
		return
	}
	top, bottom, radius := cy-barHeight/2, cy+barHeight/2, barHeight/2.0
	p.rect(x0, top, x1, bottom, pale, radius)
	if window.UsedPercent == nil || *window.UsedPercent == 0 {
		return
	}
	used := raw0 + (raw1-raw0)*min(*window.UsedPercent, 100)/100
	nowX := a.x(now)
	p.ClipRRect(x0, top, x1, bottom, radius)
	p.rect(x0, top, min(used, nowX), bottom, usedColor, 0)
	if used > nowX {
		p.rect(nowX, top, used, bottom, over, 0)
	}
	p.Restore()
}

// drawRoutingCell shows a weight or priority, or an em dash when absent.
func drawRoutingCell(p painter, x, cy float64, value *int64) {
	if value == nil {
		p.text(x, cy, "—", 14, muted, Bold, alignRight)
		return
	}
	p.text(x, cy, strconv.FormatInt(*value, 10), 14, ink, Bold, alignRight)
}

func drawValueCell(p painter, x, cy float64, window *QuotaWindow, now float64) {
	if window == nil {
		p.text(x, cy, "—", 14, muted, Bold, alignRight)
		return
	}
	if window.UsedPercent == nil {
		p.text(x, cy-7, "?", 14, muted, Bold, alignRight)
	} else {
		remaining := max(0, 100-*window.UsedPercent)
		color := ink
		if remaining < 10 {
			color = over
		}
		p.text(x, cy-7, percentText(remaining), 14, color, Bold, alignRight)
	}
	countdown := "—"
	switch {
	case window.ResetAt == nil:
		if window.UsedPercent != nil && *window.UsedPercent == 0 {
			countdown = "未开始"
		}
	case *window.ResetAt <= now:
		countdown = "待刷新"
	default:
		countdown = durationText(*window.ResetAt - now)
	}
	p.text(x, cy+9, countdown, 11, sub, Regular, alignRight)
}

func drawDateHead(p painter, a axis, cy, top, bottom, now float64) {
	days := a.days()
	todayIndex := int(math.Round((dayStart(now) - a.start) / dayLength))
	width := (a.x1 - a.x0) / float64(days)
	p.rect(a.x0+float64(todayIndex)*width, top, a.x0+float64(todayIndex+1)*width, bottom, today, 0)
	step := int(math.Ceil(44 / width)) // room for one MM/DD label
	for day := range days + 1 {
		x := a.x0 + float64(day)*width
		labelled := (day-todayIndex)%step == 0
		if width >= 12 || labelled {
			p.line(x, top, x, bottom, grid, 1)
		}
		if day < days && labelled {
			label := dateText(a.start+float64(day)*dayLength, "01/02")
			if day == todayIndex {
				label = "今天"
			}
			p.text(x+width/2, cy, label, 11, sub, Regular, alignCenter)
		}
	}
}

func drawHourHead(p painter, a axis, cy, top, bottom float64) {
	for hour := math.Ceil(a.start/hourLength) * hourLength; hour <= a.end; hour += hourLength {
		x := a.x(hour)
		p.line(x, top, x, bottom, grid, 1)
		p.line(x, top, x, top+4, muted, 1)
		if int64(math.Round(hour/hourLength))%2 == 0 && a.x0+14 <= x && x <= a.x1-14 {
			p.text(x, cy, dateText(hour, "15:00"), 11, sub, Regular, alignCenter)
		}
	}
}

func drawNowLine(p painter, a axis, top, bottom, now float64) {
	x := a.x(now)
	if x < a.x0 || x > a.x1 {
		return
	}
	p.line(x, top, x, bottom, nowColor, 1.5)
	var marker path
	marker.moveTo(x-4, top-5)
	marker.lineTo(x+4, top-5)
	marker.lineTo(x, top)
	marker.close()
	p.FillPath(&marker, nowColor)
}

func poolLabel(pool ResetPool) string {
	switch {
	case strings.HasPrefix(pool.Label, "周"):
		return "周"
	case strings.HasPrefix(pool.Label, "5h"):
		return "5h"
	}
	return ""
}

// drawMonthlyCell shows Grok's monthly allowance: remaining share, then spend
// and countdown.
func drawMonthlyCell(p painter, cy float64, row *QuotaRow, now float64) {
	window := row.Monthly
	if window == nil {
		note, color := "—", muted
		if row.ResetNote != "" {
			note, color = row.ResetNote, over
		}
		p.text(cardLabel, cy, p.fit(note, right-cardLabel, 12, Regular), 12, color, Regular, alignLeft)
		return
	}
	x := cardLabel + p.text(cardLabel, cy-7, "月", 11, muted, Regular, alignLeft) + 6
	if window.UsedPercent == nil {
		x += p.text(x, cy-7, "?", 14, muted, Bold, alignLeft)
	} else {
		remaining := max(0, 100-*window.UsedPercent)
		color := ink
		if remaining < 10 {
			color = over
		}
		x += p.text(x, cy-7, percentText(remaining), 14, color, Bold, alignLeft)
	}
	if row.ResetNote != "" {
		x += 10
		p.text(x, cy-7, p.fit(row.ResetNote, right-x, 11, Regular), 11, over, Regular, alignLeft)
	}
	var parts []string
	if window.Detail != "" {
		parts = append(parts, window.Detail)
	}
	if window.ResetAt != nil {
		if *window.ResetAt <= now {
			parts = append(parts, "待刷新")
		} else {
			parts = append(parts, durationText(*window.ResetAt-now))
		}
	}
	if len(parts) > 0 {
		line := p.fit(strings.Join(parts, " · "), right-cardLabel, 11, Regular)
		p.text(cardLabel, cy+9, line, 11, sub, Regular, alignLeft)
	}
}

func drawQuotaBuckets(p painter, y float64, buckets []QuotaBucket, now float64) {
	x0 := priorityValue + 24
	columnWidth := (right - x0) / quotaBucketColumns
	for index, bucket := range buckets {
		x := x0 + float64(index%quotaBucketColumns)*columnWidth
		cy := y + (float64(index/quotaBucketColumns)+0.5)*rowHeight
		x1 := x + columnWidth - 16
		name := p.fit(bucket.Name, columnWidth-98, 12, Regular)
		p.text(x, cy-7, name, 12, ink, Regular, alignLeft)
		label, color := "额度未知", muted
		if bucket.RemainingPercent != nil && !bucket.Disabled {
			label, color = "余 "+percentText(*bucket.RemainingPercent), ink
			if *bucket.RemainingPercent < 10 {
				color = over
			}
		}
		if bucket.Disabled {
			label = "额度不可用"
		}
		p.text(x1, cy-7, label, 13, color, Bold, alignRight)
		p.rect(x, cy+6, x+88, cy+12, grid, 3)
		if bucket.RemainingPercent != nil && !bucket.Disabled {
			p.rect(x, cy+6, x+88, cy+12, fivePale, 3)
			used := (100 - *bucket.RemainingPercent) / 100
			usedColor := five
			if *bucket.RemainingPercent < 10 {
				usedColor = over
			}
			if used > 0 {
				p.rect(x, cy+6, x+88*used, cy+12, usedColor, 3)
			}
		}
		reset := "重置时间未知"
		if bucket.ResetAt != nil {
			reset = "待刷新"
			if *bucket.ResetAt > now {
				reset = durationText(*bucket.ResetAt-now) + "后重置"
			}
		}
		p.text(x+100, cy+9, p.fit(reset, x1-x-100, 11, Regular), 11, sub, Regular, alignLeft)
	}
}

// cardChip is the cards of one bin on the card axis.
type cardChip struct {
	x       float64
	count   int
	soonest float64
}

// poolChips groups a pool's cards into binDays-day bins, in axis order.
func poolChips(pool ResetPool, a axis, binDays int, dayWidth float64) []cardChip {
	bins := map[int]*cardChip{}
	var order []int
	for _, stamp := range pool.ExpiresAt {
		day := int(math.Round((dayStart(stamp) - a.start) / dayLength))
		index := day / binDays
		bin := bins[index]
		if bin == nil {
			bin = &cardChip{soonest: stamp}
			bins[index] = bin
			order = append(order, index)
		}
		bin.count++
		bin.soonest = min(bin.soonest, stamp)
	}
	slices.Sort(order)
	span := float64(binDays) * dayWidth
	chips := make([]cardChip, 0, len(order))
	for _, index := range order {
		chip := *bins[index]
		chip.x = a.x0 + (float64(index)+0.5)*span
		chips = append(chips, chip)
	}
	return chips
}

// drawCardCell shows the count per card type, then expiry chips on the shared
// card axis.
func drawCardCell(p painter, cy float64, row *QuotaRow, a axis, now float64) {
	anyCards := slices.ContainsFunc(row.ResetPools, func(pool ResetPool) bool {
		return pool.Count != nil && *pool.Count != 0
	})
	if row.ResetNote != "" && !anyCards {
		x := min(a.clamp(a.x(now))+8, right-80)
		p.text(x, cy, p.fit(row.ResetNote, right-x, 12, Regular), 12, over, Regular, alignLeft)
		return
	}
	dayWidth := (a.x1 - a.x0) / float64(a.days())
	binDays := max(1, int(math.Ceil(chipGap/dayWidth)))
	nowX := a.x(now)
	for index, pool := range row.ResetPools {
		offset := 0.0
		if len(row.ResetPools) > 1 {
			offset = laneOffset
			if index == 0 {
				offset = -laneOffset
			}
		}
		y := cy + offset
		if len(row.ResetPools) > 1 {
			p.text(cardLabel, y, poolLabel(pool), 11, muted, Regular, alignLeft)
		}
		count, color := "?", muted
		if pool.Count != nil {
			count = strconv.Itoa(*pool.Count)
			if *pool.Count != 0 {
				color = ink
			}
		}
		p.text(cardValue, y, count, 13, color, Bold, alignRight)
		chips := poolChips(pool, a, binDays, dayWidth)
		if len(chips) == 0 {
			continue
		}
		p.line(min(nowX, chips[0].x), y, chips[len(chips)-1].x, y, cardLane, 2)
		for _, chip := range chips {
			remaining := chip.soonest - now
			chipColor := card
			if remaining < dayLength {
				chipColor = over
			} else if remaining < 3*dayLength {
				chipColor = warn
			}
			radius := 4.0
			if chip.count > 1 {
				radius = 6.5
			}
			p.circle(chip.x, y, radius+1.5, panel)
			p.circle(chip.x, y, radius, chipColor)
			if chip.count > 1 {
				p.text(chip.x, y, strconv.Itoa(chip.count), 9, panel, Bold, alignCenter)
			}
			if remaining > 0 && remaining < dayLength {
				// Away from the other lane: above the top one, below the bottom.
				labelY := y - (radius + 6)
				if offset > 0 {
					labelY = y + radius + 6
				}
				p.text(chip.x, labelY, durationText(remaining), 9.5, over, Bold, alignCenter)
			}
		}
	}
}

func drawLegend(p painter, titleY float64) {
	x := quotaWidth - margin - 4
	for _, item := range []struct {
		label string
		line  bool
		color Color
	}{
		{"现在", true, nowColor},
		{"超出进度", false, over},
		{"已用", false, week},
		{"剩余", false, weekPale},
	} {
		x -= p.measure(item.label, 12, Regular)
		p.text(x, titleY+2, item.label, 12, sub, Regular, alignLeft)
		if item.line {
			p.line(x-8, titleY-5, x-8, titleY+9, item.color, 1.5)
			x -= 30
		} else {
			p.rect(x-20, titleY-3, x-6, titleY+7, item.color, 3)
			x -= 36
		}
	}
}

// blankRowCells paints over the grid from x to the right edge of a row, keeping
// its rules, so the row can show something other than the axes.
func blankRowCells(p painter, x, y, height, rowsBottom float64) {
	rowBottom := y + height
	p.rect(x, y-1, right, rowBottom+1, panel, 0)
	p.line(x, y, right, y, rule, 1)
	if rowBottom < rowsBottom {
		p.line(x, rowBottom, right, rowBottom, rule, 1)
	}
}

// Quota renders the overview as a PNG: one section per supported channel in the
// given order. Unsupported types are left out, failed channels keep their row
// with the error printed, and groups in hidden get no tag.
func Quota(rows []QuotaRow, now float64, fontPath string, hidden map[string]bool) ([]byte, error) {
	supported := make([]QuotaRow, 0, len(rows))
	live := make([]QuotaRow, 0, len(rows))
	for _, row := range rows {
		if row.Unsupported {
			continue
		}
		supported = append(supported, row)
		if row.Issue == "" && len(row.QuotaBuckets) == 0 {
			live = append(live, row)
		}
	}
	body := 0.0
	for _, row := range supported {
		body += row.height()
	}
	if len(supported) == 0 {
		body = 64
	}
	height := margin + titleHeight + headHeight + body + 8 + margin
	if height > maxQuotaImage {
		return nil, errors.New("渠道过多，单张额度图片过高")
	}
	faces, err := loadFaces(fontPath)
	if err != nil {
		return nil, err
	}
	p := painter{newCanvas(faces, quotaWidth, int(height), 2, pageBG)}

	titleY := margin + 22
	width := p.text(margin+4, titleY, "订阅额度", 24, ink, Bold, alignLeft)
	p.text(margin+18+width, titleY+2, dateText(now, "01/02 15:04"), 14, sub, Regular, alignLeft)
	drawLegend(p, titleY)

	top := margin + titleHeight
	p.rect(margin, top, quotaWidth-margin, height-margin, panel, 14)
	head := top + headHeight/2 + 1
	rowsTop := top + headHeight
	rowsBottom := rowsTop + body
	weekScale := weekAxis(live, now)
	fiveScale := axis{now - 5*hourLength, now + 5*hourLength, fiveX0, fiveX1}
	cardScale := cardAxis(live, now)
	p.text(weightValue, head, "W", 12, sub, Bold, alignRight)
	p.text(priorityValue, head, "P", 12, sub, Bold, alignRight)
	quotaOnly := len(live) == 0 && slices.ContainsFunc(supported, func(row QuotaRow) bool {
		return row.Issue == "" && len(row.QuotaBuckets) > 0
	})
	if quotaOnly {
		p.text(priorityValue+24, head, "模型组额度 · 剩余比例 / 重置时间", 12, sub, Bold, alignLeft)
	} else {
		p.text(weekValue, head, "周", 12, sub, Bold, alignRight)
		p.text(fiveValue, head, "5h", 12, sub, Bold, alignRight)
		p.text(cardValue, head, "重置卡", 12, sub, Bold, alignRight)
	}
	if len(live) > 0 {
		drawDateHead(p, weekScale, head, rowsTop, rowsBottom, now)
		drawHourHead(p, fiveScale, head, rowsTop, rowsBottom)
		drawDateHead(p, cardScale, head, rowsTop, rowsBottom, now)
	}
	y := rowsTop
	for i := range supported {
		row := &supported[i]
		p.line(left, y, right, y, rule, 1)
		cy := y + rowHeight/2
		drawName(p, cy-9, row, hidden)
		drawRoutingCell(p, weightValue, cy, row.Weight)
		drawRoutingCell(p, priorityValue, cy, row.Priority)
		if row.Issue == "" && len(row.QuotaBuckets) == 0 {
			drawValueCell(p, weekValue, cy, row.Weekly, now)
			drawValueCell(p, fiveValue, cy, row.FiveHour, now)
			drawWindowBar(p, row.Weekly, cy, weekScale, now, week, weekPale)
			drawWindowBar(p, row.FiveHour, cy, fiveScale, now, five, fivePale)
			if row.Provider != ProviderGrok {
				drawCardCell(p, cy, row, cardScale, now)
			}
		}
		y += row.height()
	}
	if len(live) > 0 {
		for _, scale := range []axis{weekScale, fiveScale, cardScale} {
			drawNowLine(p, scale, rowsTop, rowsBottom, now)
		}
	}
	// Second pass for rows that blank part of the grid: failed rows paint over
	// all three axes to print their error or grouped quotas, and Grok rows blank
	// the card axis for their monthly cell. This runs after the now lines.
	y = rowsTop
	for i := range supported {
		row := &supported[i]
		if row.Issue != "" {
			x0 := priorityValue + 12
			blankRowCells(p, x0, y, row.height(), rowsBottom)
			issue := p.fit(row.Issue, right-x0-12, 13, Regular)
			p.text(x0+12, y+rowHeight/2, issue, 13, over, Regular, alignLeft)
		} else if len(row.QuotaBuckets) > 0 {
			blankRowCells(p, priorityValue+12, y, row.height(), rowsBottom)
			drawQuotaBuckets(p, y, row.QuotaBuckets, now)
		} else if row.Provider == ProviderGrok {
			blankRowCells(p, cardLabel-6, y, row.height(), rowsBottom)
			drawMonthlyCell(p, y+rowHeight/2, row, now)
		}
		y += row.height()
	}
	if len(supported) == 0 {
		p.text(quotaWidth/2.0, y+32, "暂无可展示的订阅额度", 15, muted, Regular, alignCenter)
	}
	return p.PNG()
}
