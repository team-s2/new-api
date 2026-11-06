package render

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// The flow image is a light Sankey diagram in the visual style of the Dashboard
// Flow chart: one column per stage, ribbon width proportional to token_used.

// FlowOverflowHide drops paths outside the Top N instead of merging them.
const FlowOverflowHide = "hide"

// FlowRow is one aggregated row of /api/data/flow.
type FlowRow struct {
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	NodeName    string `json:"node_name"`
	TokenID     int    `json:"token_id"`
	TokenName   string `json:"token_name"`
	UseGroup    string `json:"use_group"`
	ChannelID   int    `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	ModelName   string `json:"model_name"`
	TokenUsed   int    `json:"token_used"`
}

// FlowOptions selects what the diagram shows.
type FlowOptions struct {
	Stages   []string
	TopLimit int
	Overflow string
	FontPath string
}

var flowPalette = []Color{
	0xFF48C98E, 0xFF2F72F6, 0xFFF9C51B, 0xFF42C6E9,
	0xFFFF8B18, 0xFFD2B5F3, 0xFF8252DF, 0xFF9FC5F8,
	0xFFFFC565, 0xFFB8E8CD, 0xFFFFE578, 0xFF38557D,
}

var otherLabels = map[string]string{
	"user":    "Other users",
	"node":    "Other nodes",
	"token":   "Other tokens",
	"group":   "Other groups",
	"model":   "Other models",
	"channel": "Other channels",
}

const (
	trunk            = Color(0xFF9AA3B2) // single nodes ahead of the colour-giving stage
	minFlowWidth     = 3600
	minFlowHeight    = 2240
	maxFlowPixels    = 64_000_000
	flowFont         = 40.0
	flowMarginX      = 80.0
	flowMarginY      = 80.0
	panelInset       = 32.0 // white panel edge inside the light page
	nodeRadius       = 6.0
	valueGap         = 14.0 // between a node's name and its token count
	nodeWidth        = 56.0
	nodeGap          = 14.0
	minNodeHeight    = 8.0
	labelPadding     = 8.0
	columnGap        = 120.0
	labelLineGap     = 56.0
	proportionalFlow = 1120.0
	flowNameLimit    = 22
	pathSeparator    = "\x00"
)

type flowNode struct {
	id, label, kind string
}

func rowNode(row FlowRow, stage string) flowNode {
	var label, identity string
	switch stage {
	case "user":
		label = row.Username
		if label == "" {
			label = "Unknown User"
			if row.UserID != 0 {
				label = fmt.Sprintf("user-%d", row.UserID)
			}
		}
		identity = label
		if row.UserID != 0 {
			identity = strconv.Itoa(row.UserID)
		}
	case "node":
		label = cmp.Or(row.NodeName, "default-node")
		identity = label
	case "token":
		label = cmp.Or(row.TokenName, fmt.Sprintf("Deleted token #%d", row.TokenID))
		identity = strconv.Itoa(row.TokenID)
	case "group":
		label = cmp.Or(row.UseGroup, "default")
		identity = label
	case "model":
		label = cmp.Or(row.ModelName, "Unknown model")
		identity = label
	default:
		label = cmp.Or(row.ChannelName, fmt.Sprintf("Channel #%d", row.ChannelID))
		identity = strconv.Itoa(row.ChannelID)
	}
	return flowNode{id: stage + ":" + identity, label: label, kind: stage}
}

// compactTokens formats a token total for a node label, e.g. 1.6M.
func compactTokens(value float64) string {
	for _, unit := range []struct {
		divisor float64
		suffix  string
	}{{1_000_000_000, "B"}, {1_000_000, "M"}, {1_000, "K"}} {
		if value >= unit.divisor {
			scaled := value / unit.divisor
			digits := 1
			if scaled >= 100 {
				digits = 0
			}
			return strconv.FormatFloat(scaled, 'f', digits, 64) + unit.suffix
		}
	}
	return strconv.FormatFloat(value, 'f', 0, 64)
}

func nodeName(label string) string {
	runes := []rune(label)
	if len(runes) <= flowNameLimit {
		return label
	}
	return string(runes[:flowNameLimit-1]) + ellipsis
}

// flowPath is one aggregated route through every stage.
type flowPath struct {
	ids   []string
	value float64
}

// aggregateFlow keeps each stage's Top N nodes, merges or hides the rest, and
// returns the distinct paths with the nodes they use.
func aggregateFlow(rows []FlowRow, options FlowOptions) ([]flowPath, map[string]flowNode) {
	type preparedRow struct {
		path  []flowNode
		value float64
	}
	stages := options.Stages
	prepared := make([]preparedRow, 0, len(rows))
	stageTotals := make([]map[string]float64, len(stages))
	for i := range stageTotals {
		stageTotals[i] = map[string]float64{}
	}
	for _, row := range rows {
		if row.TokenUsed <= 0 {
			continue
		}
		value := float64(row.TokenUsed)
		path := make([]flowNode, len(stages))
		for i, stage := range stages {
			path[i] = rowNode(row, stage)
			stageTotals[i][path[i].id] += value
		}
		prepared = append(prepared, preparedRow{path, value})
	}

	topIDs := make([]map[string]bool, len(stages))
	for i, totals := range stageTotals {
		ordered := make([]string, 0, len(totals))
		for id := range totals {
			ordered = append(ordered, id)
		}
		slices.SortFunc(ordered, func(a, b string) int {
			return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a, b))
		})
		topIDs[i] = map[string]bool{}
		for _, id := range ordered[:min(options.TopLimit, len(ordered))] {
			topIDs[i][id] = true
		}
	}

	nodes := map[string]flowNode{}
	merged := map[string]*flowPath{}
	var paths []*flowPath
	for _, row := range prepared {
		overflowed := false
		ids := make([]string, len(row.path))
		normalized := make([]flowNode, len(row.path))
		for i, node := range row.path {
			if !topIDs[i][node.id] {
				overflowed = true
				node = flowNode{id: node.kind + ":__other__", label: otherLabels[node.kind], kind: node.kind}
			}
			ids[i], normalized[i] = node.id, node
		}
		if overflowed && options.Overflow == FlowOverflowHide {
			continue
		}
		key := strings.Join(ids, pathSeparator)
		path := merged[key]
		if path == nil {
			path = &flowPath{ids: ids}
			merged[key] = path
			paths = append(paths, path)
		}
		path.value += row.value
		for _, node := range normalized {
			nodes[node.id] = node
		}
	}
	result := make([]flowPath, len(paths))
	for i, path := range paths {
		result[i] = *path
	}
	return result, nodes
}

// flowColors assigns each node and link the colour that contributes most to it.
// Colour comes from the first stage that actually branches: a lone user or node
// would otherwise paint the whole diagram in a single colour.
func flowColors(paths []flowPath, nodes map[string]flowNode, stageCount int) (map[string]Color, map[string]Color) {
	colorStage := 0
	for stage := range stageCount {
		distinct := map[string]bool{}
		for _, path := range paths {
			distinct[path.ids[stage]] = true
		}
		if len(distinct) > 1 {
			colorStage = stage
			break
		}
	}
	colorTotals := map[string]float64{}
	for _, path := range paths {
		colorTotals[path.ids[colorStage]] += path.value
	}
	// The palette runs bottom-up through that column, which is laid out largest
	// first, so the first colour lands on the bottom node.
	colorIDs := make([]string, 0, len(colorTotals))
	for id := range colorTotals {
		colorIDs = append(colorIDs, id)
	}
	slices.SortFunc(colorIDs, func(a, b string) int {
		return cmp.Or(cmp.Compare(colorTotals[a], colorTotals[b]), cmp.Compare(nodes[b].label, nodes[a].label), cmp.Compare(b, a))
	})
	rootColors := make(map[string]Color, len(colorIDs))
	for i, id := range colorIDs {
		rootColors[id] = flowPalette[i%len(flowPalette)]
	}

	nodeVotes := map[string]map[Color]float64{}
	linkVotes := map[string]map[Color]float64{}
	vote := func(votes map[string]map[Color]float64, key string, color Color, value float64) {
		if votes[key] == nil {
			votes[key] = map[Color]float64{}
		}
		votes[key][color] += value
	}
	for _, path := range paths {
		color := rootColors[path.ids[colorStage]]
		for i, id := range path.ids {
			nodeColor := trunk
			if i >= colorStage {
				nodeColor = color
			}
			vote(nodeVotes, id, nodeColor, path.value)
			if i+1 < len(path.ids) {
				vote(linkVotes, id+pathSeparator+path.ids[i+1], color, path.value)
			}
		}
	}
	dominant := func(votes map[string]map[Color]float64) map[string]Color {
		result := make(map[string]Color, len(votes))
		for key, candidates := range votes {
			var best Color
			bestValue := -1.0
			for color, value := range candidates {
				if value > bestValue || value == bestValue && color > best {
					best, bestValue = color, value
				}
			}
			result[key] = best
		}
		return result
	}
	return dominant(nodeVotes), dominant(linkVotes)
}

// flowPosition is a laid-out node.
type flowPosition struct {
	id     string
	x      float64
	y0, y1 float64
	value  float64
	color  Color
}

// flowRibbon is a laid-out link between two adjacent stages.
type flowRibbon struct {
	x0, x1             float64
	sy0, sy1, ty0, ty1 float64
	color              Color
	alpha              float64
	value              float64
}

// layoutRibbons stacks a stage's links inside its nodes, in node order.
func layoutRibbons(totals map[[2]string]float64, sources, targets map[string]*flowPosition, linkColors map[string]Color) []flowRibbon {
	keys := make([][2]string, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	bySource := map[string][][2]string{}
	for _, key := range keys {
		bySource[key[0]] = append(bySource[key[0]], key)
	}
	alphas := make(map[[2]string]float64, len(totals))
	for _, sourceLinks := range bySource {
		slices.SortFunc(sourceLinks, func(a, b [2]string) int {
			return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a[1], b[1]))
		})
		denominator := float64(max(len(sourceLinks)-1, 1))
		for i, key := range sourceLinks {
			alphas[key] = 0.24 + float64(i)/denominator*0.2
			if len(sourceLinks) == 1 {
				alphas[key] = 0.34
			}
		}
	}
	sourceCursor := make(map[string]float64, len(sources))
	for id, node := range sources {
		sourceCursor[id] = node.y0
	}
	targetCursor := make(map[string]float64, len(targets))
	for id, node := range targets {
		targetCursor[id] = node.y0
	}
	slices.SortFunc(keys, func(a, b [2]string) int {
		return cmp.Or(cmp.Compare(sources[a[0]].y0, sources[b[0]].y0), cmp.Compare(targets[a[1]].y0, targets[b[1]].y0))
	})
	ribbons := make([]flowRibbon, 0, len(keys))
	for _, key := range keys {
		value := totals[key]
		source, target := sources[key[0]], targets[key[1]]
		sourceHeight := (source.y1 - source.y0) * value / source.value
		targetHeight := (target.y1 - target.y0) * value / target.value
		ribbons = append(ribbons, flowRibbon{
			x0:    source.x + nodeWidth,
			x1:    target.x,
			sy0:   sourceCursor[key[0]],
			sy1:   sourceCursor[key[0]] + sourceHeight,
			ty0:   targetCursor[key[1]],
			ty1:   targetCursor[key[1]] + targetHeight,
			color: linkColors[key[0]+pathSeparator+key[1]],
			alpha: alphas[key],
			value: value,
		})
		sourceCursor[key[0]] += sourceHeight
		targetCursor[key[1]] += targetHeight
	}
	return ribbons
}

func withAlpha(c Color, alpha float64) Color {
	return c&0x00FFFFFF | Color(math.Round(alpha*255))<<24
}

// Flow renders flow rows as a PNG Sankey diagram. Stages must already be in
// display order.
func Flow(rows []FlowRow, options FlowOptions) ([]byte, error) {
	stages := options.Stages
	if len(stages) < 2 {
		return nil, errors.New("流图至少需要两个阶段")
	}
	paths, nodes := aggregateFlow(rows, options)
	if len(paths) == 0 {
		return nil, errors.New("所选时间范围内没有流图数据")
	}
	nodeColors, linkColors := flowColors(paths, nodes, len(stages))

	nodeTotals := make([]map[string]float64, len(stages))
	for i := range nodeTotals {
		nodeTotals[i] = map[string]float64{}
	}
	linkTotals := make([]map[[2]string]float64, len(stages)-1)
	for i := range linkTotals {
		linkTotals[i] = map[[2]string]float64{}
	}
	for _, path := range paths {
		for i, id := range path.ids {
			nodeTotals[i][id] += path.value
			if i+1 < len(path.ids) {
				linkTotals[i][[2]string{id, path.ids[i+1]}] += path.value
			}
		}
	}

	faces, err := loadFaces(options.FontPath)
	if err != nil {
		return nil, err
	}
	measure := painter{newCanvas(faces, 1, 1, 1, pageBG)}
	ascent, descent := measure.Metrics(flowFont, Regular)
	labelHeight := descent - ascent
	lineGap := max(labelLineGap, labelHeight+8)

	stageLabelWidths := make([]float64, len(stages))
	totalLabelWidth := 0.0
	maxStageNodes := 0
	for i, totals := range nodeTotals {
		for id, value := range totals {
			width := measure.measure(nodeName(nodes[id].label), flowFont, Regular) +
				valueGap + measure.measure(compactTokens(value), flowFont, Bold)
			stageLabelWidths[i] = max(stageLabelWidths[i], width)
		}
		totalLabelWidth += stageLabelWidths[i]
		maxStageNodes = max(maxStageNodes, len(totals))
	}

	columns := float64(len(stages))
	naturalWidth := 2*flowMarginX + columns*(nodeWidth+labelPadding) + totalLabelWidth + (columns-1)*columnGap
	width := max(minFlowWidth, int(math.Ceil(naturalWidth)))
	extraColumnGap := (float64(width) - naturalWidth) / (columns - 1)
	nodeX := make([]float64, len(stages))
	nodeX[0] = flowMarginX
	for i := 1; i < len(stages); i++ {
		nodeX[i] = nodeX[i-1] + nodeWidth + labelPadding + stageLabelWidths[i-1] + columnGap + extraColumnGap
	}

	stageNodes := float64(maxStageNodes)
	nodeHeight := max(minNodeHeight, lineGap-nodeGap)
	labelArea := labelHeight + lineGap*(stageNodes-1)
	nodeArea := nodeHeight*stageNodes + nodeGap*(stageNodes-1) + proportionalFlow
	height := max(minFlowHeight, int(math.Ceil(2*flowMarginY+max(labelArea, nodeArea))))
	if width*height > maxFlowPixels {
		return nil, fmt.Errorf("流图需要 %s 像素，超过 %s 像素的安全限制；请减少 Top N 或可见阶段",
			groupDigits(width*height), groupDigits(maxFlowPixels))
	}

	// Columns of nodes, each ordered top to bottom.
	columnsOfNodes := make([][]*flowPosition, len(stages))
	positions := make([]map[string]*flowPosition, len(stages))
	for i, totals := range nodeTotals {
		ordered := make([]string, 0, len(totals))
		total := 0.0
		for id, value := range totals {
			ordered = append(ordered, id)
			total += value
		}
		slices.SortFunc(ordered, func(a, b string) int {
			return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(nodes[a].label, nodes[b].label), cmp.Compare(a, b))
		})
		count := float64(len(ordered))
		available := float64(height) - 2*flowMarginY - nodeGap*(count-1)
		baseline := min(nodeHeight, available/count)
		flexible := max(available-baseline*count, 0)
		cursor := flowMarginY
		positions[i] = make(map[string]*flowPosition, len(ordered))
		for _, id := range ordered {
			span := baseline + flexible*totals[id]/total
			node := &flowPosition{id: id, x: nodeX[i], y0: cursor, y1: cursor + span, value: totals[id], color: nodeColors[id]}
			positions[i][id] = node
			columnsOfNodes[i] = append(columnsOfNodes[i], node)
			cursor += span + nodeGap
		}
	}

	var ribbons []flowRibbon
	for i, totals := range linkTotals {
		ribbons = append(ribbons, layoutRibbons(totals, positions[i], positions[i+1], linkColors)...)
	}

	p := painter{newCanvas(faces, width, height, 1, pageBG)}
	p.rect(panelInset, panelInset, float64(width)-panelInset, float64(height)-panelInset, panel, 28)

	// Largest ribbons first so thin ones stay visible on top.
	slices.SortStableFunc(ribbons, func(a, b flowRibbon) int { return cmp.Compare(b.value, a.value) })
	for _, ribbon := range ribbons {
		control := (ribbon.x1 - ribbon.x0) * 0.48
		var outline path
		outline.moveTo(ribbon.x0, ribbon.sy0)
		outline.cubicTo(ribbon.x0+control, ribbon.sy0, ribbon.x1-control, ribbon.ty0, ribbon.x1, ribbon.ty0)
		outline.lineTo(ribbon.x1, ribbon.ty1)
		outline.cubicTo(ribbon.x1-control, ribbon.ty1, ribbon.x0+control, ribbon.sy1, ribbon.x0, ribbon.sy1)
		outline.close()
		p.FillPath(&outline, withAlpha(ribbon.color, ribbon.alpha))
	}

	labelTop := flowMarginY + labelHeight/2
	labelBottom := float64(height) - flowMarginY - labelHeight/2
	for _, column := range columnsOfNodes {
		// Labels follow their nodes but keep one line apart, pushed up from the
		// bottom edge if the column runs out of room.
		centers := make([]float64, len(column))
		for i, node := range column {
			floor := labelTop
			if i > 0 {
				floor = centers[i-1] + lineGap
			}
			centers[i] = max((node.y0+node.y1)/2, floor)
		}
		if last := len(centers) - 1; centers[last] > labelBottom {
			centers[last] = labelBottom
			for i := last - 1; i >= 0; i-- {
				centers[i] = min(centers[i], centers[i+1]-lineGap)
			}
		}
		for i, node := range column {
			radius := min(nodeRadius, (node.y1-node.y0)/2)
			p.rect(node.x, node.y0, node.x+nodeWidth, node.y1, node.color, radius)
			labelX := node.x + nodeWidth + labelPadding
			labelX += p.text(labelX, centers[i], nodeName(nodes[node.id].label), flowFont, ink, Regular, alignLeft)
			p.text(labelX+valueGap, centers[i], compactTokens(node.value), flowFont, sub, Bold, alignLeft)
		}
	}
	return p.PNG()
}

// groupDigits inserts thousands separators.
func groupDigits(value int) string {
	digits := strconv.Itoa(value)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
