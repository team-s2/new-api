package render

import (
	"regexp"
	"strings"
)

const (
	ink = Color(0xFF1D2230)
	// unitGap marks a narrow gap. Noto has no glyph for U+2005, so text is
	// split there and the gap is drawn as space; layouts put it around
	// 天/时/分 to set units slightly apart from numbers.
	unitGap = "\u2005"
	gapEm   = 1.0 / 6
	// ellipsis ends text cut to fit a width.
	ellipsis = "…"
)

type align int

const (
	alignLeft align = iota
	alignCenter
	alignRight
)

// painter adds the layouts' text conventions to a canvas.
type painter struct {
	*canvas
}

func (p painter) measure(value string, size float64, w Weight) float64 {
	pieces := strings.Split(value, unitGap)
	width := float64(len(pieces)-1) * size * gapEm
	for _, piece := range pieces {
		width += p.MeasureText(piece, size, w)
	}
	return width
}

var whitespace = regexp.MustCompile(`[ \t\r\n]+`)

// fit collapses whitespace and cuts value, ending it with an ellipsis, until
// it fits width.
func (p painter) fit(value string, width, size float64, w Weight) string {
	value = strings.TrimSpace(whitespace.ReplaceAllString(value, " "))
	if p.measure(value, size, w) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && p.measure(string(runes)+ellipsis, size, w) > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ellipsis
}

// text draws value vertically centred on cy and returns its width.
func (p painter) text(x, cy float64, value string, size float64, c Color, w Weight, a align) float64 {
	width := p.measure(value, size, w)
	switch a {
	case alignRight:
		x -= width
	case alignCenter:
		x -= width / 2
	}
	ascent, descent := p.Metrics(size, w)
	baseline := cy - (ascent+descent)/2
	for _, piece := range strings.Split(value, unitGap) {
		p.DrawText(piece, x, baseline, size, w, c)
		x += p.MeasureText(piece, size, w) + size*gapEm
	}
	return width
}

func (p painter) rect(x0, y0, x1, y1 float64, c Color, radius float64) {
	p.FillRRect(x0, y0, x1, y1, radius, c)
}

func (p painter) line(x0, y0, x1, y1 float64, c Color, width float64) {
	p.StrokeLine(x0, y0, x1, y1, width, c)
}

func (p painter) circle(x, y, radius float64, c Color) {
	p.FillCircle(x, y, radius, c)
}
