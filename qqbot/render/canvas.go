package render

// canvas rasterizes with golang.org/x/image: sfnt supplies glyph outlines and
// metrics, and vector fills every outline with exact-area anti-aliasing.

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	xvector "golang.org/x/image/vector"
)

var buffers = sync.Pool{New: func() any { return new(sfnt.Buffer) }}

// canvas is a raster image addressed in logical pixels, which it multiplies
// by its scale. Fills are anti-aliased and lines have round caps. Text is
// laid out glyph by glyph without shaping or kerning.
type canvas struct {
	faces *faceSet
	img   *image.RGBA
	scale float64
	clips []*image.Alpha
}

// newCanvas returns a width×height logical-pixel canvas drawn at scale and
// cleared to background.
func newCanvas(faces *faceSet, width, height int, scale float64, background Color) *canvas {
	w, h := int(math.Round(float64(width)*scale)), int(math.Round(float64(height)*scale))
	s := &canvas{faces: faces, img: image.NewRGBA(image.Rect(0, 0, w, h)), scale: scale}
	draw.Draw(s.img, s.img.Bounds(), image.NewUniform(nrgba(background)), image.Point{}, draw.Src)
	return s
}

func nrgba(c Color) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: uint8(c >> 24)}
}

// shape collects an outline in device pixels and fills it.
type shape struct {
	s                      *canvas
	ops                    []func(z *xvector.Rasterizer, dx, dy float32)
	minX, minY, maxX, maxY float64
	// curX and curY are the pen position in logical coordinates.
	curX, curY float64
}

func (s *canvas) shape() *shape {
	return &shape{s: s, minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
}

func (sh *shape) grow(x, y float64) {
	sh.minX, sh.maxX = min(sh.minX, x), max(sh.maxX, x)
	sh.minY, sh.maxY = min(sh.minY, y), max(sh.maxY, y)
}

// The methods below take logical coordinates.
func (sh *shape) moveTo(x, y float64) {
	sh.curX, sh.curY = x, y
	x, y = x*sh.s.scale, y*sh.s.scale
	sh.grow(x, y)
	sh.ops = append(sh.ops, func(z *xvector.Rasterizer, dx, dy float32) { z.MoveTo(float32(x)-dx, float32(y)-dy) })
}

func (sh *shape) lineTo(x, y float64) {
	sh.curX, sh.curY = x, y
	x, y = x*sh.s.scale, y*sh.s.scale
	sh.grow(x, y)
	sh.ops = append(sh.ops, func(z *xvector.Rasterizer, dx, dy float32) { z.LineTo(float32(x)-dx, float32(y)-dy) })
}

func (sh *shape) cubicTo(x1, y1, x2, y2, x3, y3 float64) {
	sh.curX, sh.curY = x3, y3
	sc := sh.s.scale
	x1, y1, x2, y2, x3, y3 = x1*sc, y1*sc, x2*sc, y2*sc, x3*sc, y3*sc
	sh.grow(x1, y1)
	sh.grow(x2, y2)
	sh.grow(x3, y3)
	sh.ops = append(sh.ops, func(z *xvector.Rasterizer, dx, dy float32) {
		z.CubeTo(float32(x1)-dx, float32(y1)-dy, float32(x2)-dx, float32(y2)-dy, float32(x3)-dx, float32(y3)-dy)
	})
}

func (sh *shape) close() {
	sh.ops = append(sh.ops, func(z *xvector.Rasterizer, _, _ float32) { z.ClosePath() })
}

// arc adds a quarter-circle-or-less arc around (cx, cy) from angle a0 to a1
// (radians), assuming the pen is at its start.
func (sh *shape) arc(cx, cy, r, a0, a1 float64) {
	k := 4.0 / 3 * math.Tan((a1-a0)/4)
	x0, y0 := cx+r*math.Cos(a0), cy+r*math.Sin(a0)
	x3, y3 := cx+r*math.Cos(a1), cy+r*math.Sin(a1)
	sh.cubicTo(x0-k*r*math.Sin(a0), y0+k*r*math.Cos(a0), x3+k*r*math.Sin(a1), y3-k*r*math.Cos(a1), x3, y3)
}

// mask rasterizes the shape's coverage within its pixel bounds, applying the
// current clip.
func (sh *shape) mask() (*image.Alpha, image.Rectangle) {
	if len(sh.ops) == 0 {
		return nil, image.Rectangle{}
	}
	r := image.Rect(int(math.Floor(sh.minX))-1, int(math.Floor(sh.minY))-1, int(math.Ceil(sh.maxX))+1, int(math.Ceil(sh.maxY))+1).Intersect(sh.s.img.Bounds())
	if r.Empty() {
		return nil, r
	}
	z := xvector.NewRasterizer(r.Dx(), r.Dy())
	dx, dy := float32(r.Min.X), float32(r.Min.Y)
	for _, op := range sh.ops {
		op(z, dx, dy)
	}
	m := image.NewAlpha(image.Rect(0, 0, r.Dx(), r.Dy()))
	z.Draw(m, m.Bounds(), image.Opaque, image.Point{})
	if n := len(sh.s.clips); n > 0 {
		clip := sh.s.clips[n-1]
		for y := range r.Dy() {
			for x := range r.Dx() {
				i := m.PixOffset(x, y)
				m.Pix[i] = uint8(uint16(m.Pix[i]) * uint16(clip.AlphaAt(r.Min.X+x, r.Min.Y+y).A) / 255)
			}
		}
	}
	return m, r
}

func (sh *shape) fill(c Color) {
	m, r := sh.mask()
	if m == nil {
		return
	}
	draw.DrawMask(sh.s.img, r, image.NewUniform(nrgba(c)), image.Point{}, m, image.Point{}, draw.Over)
}

// rrect adds a rounded rectangle with corner radius limited to the sides.
func (sh *shape) rrect(x0, y0, x1, y1, radius float64) {
	x0, x1 = min(x0, x1), max(x0, x1)
	y0, y1 = min(y0, y1), max(y0, y1)
	r := max(0, min(radius, (x1-x0)/2, (y1-y0)/2))
	if r == 0 {
		sh.moveTo(x0, y0)
		sh.lineTo(x1, y0)
		sh.lineTo(x1, y1)
		sh.lineTo(x0, y1)
		sh.close()
		return
	}
	sh.moveTo(x0+r, y0)
	sh.lineTo(x1-r, y0)
	sh.arc(x1-r, y0+r, r, -math.Pi/2, 0)
	sh.lineTo(x1, y1-r)
	sh.arc(x1-r, y1-r, r, 0, math.Pi/2)
	sh.lineTo(x0+r, y1)
	sh.arc(x0+r, y1-r, r, math.Pi/2, math.Pi)
	sh.lineTo(x0, y0+r)
	sh.arc(x0+r, y0+r, r, math.Pi, 3*math.Pi/2)
	sh.close()
}

func (s *canvas) FillRRect(x0, y0, x1, y1, radius float64, c Color) {
	if x0 == x1 || y0 == y1 {
		return
	}
	sh := s.shape()
	sh.rrect(x0, y0, x1, y1, radius)
	sh.fill(c)
}

// StrokeLine fills the capsule a round-capped stroke covers.
func (s *canvas) StrokeLine(x0, y0, x1, y1, width float64, c Color) {
	hw := width / 2
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	if length == 0 {
		s.FillCircle(x0, y0, hw, c)
		return
	}
	a := math.Atan2(dy, dx)
	sh := s.shape()
	nx, ny := -dy/length*hw, dx/length*hw
	sh.moveTo(x0+nx, y0+ny)
	sh.lineTo(x1+nx, y1+ny)
	sh.arc(x1, y1, hw, a+math.Pi/2, a)
	sh.arc(x1, y1, hw, a, a-math.Pi/2)
	sh.lineTo(x0-nx, y0-ny)
	sh.arc(x0, y0, hw, a-math.Pi/2, a-math.Pi)
	sh.arc(x0, y0, hw, a-math.Pi, a-3*math.Pi/2)
	sh.close()
	sh.fill(c)
}

func (s *canvas) FillCircle(x, y, radius float64, c Color) {
	sh := s.shape()
	sh.moveTo(x+radius, y)
	for i := range 4 {
		a := float64(i) * math.Pi / 2
		sh.arc(x, y, radius, a, a+math.Pi/2)
	}
	sh.close()
	sh.fill(c)
}

func (s *canvas) FillPath(p *path, c Color) {
	sh := s.shape()
	pts := p.points
	for _, verb := range p.verbs {
		switch verb {
		case verbMove:
			sh.moveTo(pts[0], pts[1])
			pts = pts[2:]
		case verbLine:
			sh.lineTo(pts[0], pts[1])
			pts = pts[2:]
		case verbCubic:
			sh.cubicTo(pts[0], pts[1], pts[2], pts[3], pts[4], pts[5])
			pts = pts[6:]
		case verbClose:
			sh.close()
		}
	}
	sh.fill(c)
}

func (s *canvas) ClipRRect(x0, y0, x1, y1, radius float64) {
	sh := s.shape()
	sh.rrect(x0, y0, x1, y1, radius)
	clip := image.NewAlpha(s.img.Bounds())
	if m, r := sh.mask(); m != nil {
		draw.Draw(clip, r, m, image.Point{}, draw.Src)
	}
	s.clips = append(s.clips, clip)
}

func (s *canvas) Restore() {
	if n := len(s.clips); n > 0 {
		s.clips = s.clips[:n-1]
	}
}

// advance is a glyph's unhinted advance at size in logical pixels.
func advance(f face, b *sfnt.Buffer, index sfnt.GlyphIndex, size float64) (float64, error) {
	a, err := f.font.GlyphAdvance(b, index, fixed.Int26_6(f.upem)<<6, font.HintingNone)
	if err != nil {
		return 0, err
	}
	return float64(a) / 64 * size / f.upem, nil
}

// glyphs calls fn with each glyph of value and its pen offset in logical
// pixels, and returns the total advance.
func glyphs(f face, value string, size float64, fn func(b *sfnt.Buffer, index sfnt.GlyphIndex, pen float64) error) (float64, error) {
	b := buffers.Get().(*sfnt.Buffer)
	defer buffers.Put(b)
	pen := 0.0
	for _, r := range value {
		index, err := f.font.GlyphIndex(b, r)
		if err != nil {
			return 0, err
		}
		if fn != nil {
			if err := fn(b, index, pen); err != nil {
				return 0, err
			}
		}
		a, err := advance(f, b, index, size)
		if err != nil {
			return 0, err
		}
		pen += a
	}
	return pen, nil
}

func (s *canvas) MeasureText(value string, size float64, w Weight) float64 {
	width, err := glyphs(s.faces[w], value, size, nil)
	if err != nil {
		return 0
	}
	return width
}

func (s *canvas) DrawText(value string, x, baseline, size float64, w Weight, c Color) {
	f := s.faces[w]
	em := size / f.upem
	upem := fixed.Int26_6(f.upem) << 6
	sh := s.shape()
	_, _ = glyphs(f, value, size, func(b *sfnt.Buffer, index sfnt.GlyphIndex, pen float64) error {
		segments, err := f.font.LoadGlyph(b, index, upem, nil)
		if err != nil {
			return err
		}
		ox := x + pen
		at := func(p fixed.Point26_6) (float64, float64) {
			return ox + float64(p.X)/64*em, baseline + float64(p.Y)/64*em
		}
		open := false
		for _, seg := range segments {
			switch seg.Op {
			case sfnt.SegmentOpMoveTo:
				if open {
					sh.close()
				}
				sh.moveTo(at(seg.Args[0]))
				open = true
			case sfnt.SegmentOpLineTo:
				sh.lineTo(at(seg.Args[0]))
			case sfnt.SegmentOpQuadTo:
				// Raise to a cubic so one path type covers both outline formats.
				x0, y0 := sh.curX, sh.curY
				x1, y1 := at(seg.Args[0])
				x2, y2 := at(seg.Args[1])
				sh.cubicTo(x0+2.0/3*(x1-x0), y0+2.0/3*(y1-y0), x2+2.0/3*(x1-x2), y2+2.0/3*(y1-y2), x2, y2)
			case sfnt.SegmentOpCubeTo:
				x1, y1 := at(seg.Args[0])
				x2, y2 := at(seg.Args[1])
				x3, y3 := at(seg.Args[2])
				sh.cubicTo(x1, y1, x2, y2, x3, y3)
			}
		}
		if open {
			sh.close()
		}
		return nil
	})
	sh.fill(c)
}

func (s *canvas) Metrics(size float64, w Weight) (float64, float64) {
	f := s.faces[w]
	return f.ascent * size / f.upem, f.descent * size / f.upem
}

func (s *canvas) PNG() ([]byte, error) {
	var out bytes.Buffer
	if err := png.Encode(&out, s.img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
