// Command genicons renders the application and tray icons. Everything is
// drawn from code (with 4x supersampling) so the assets can be regenerated at
// any time with:
//
//	go run ./tools/genicons
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

const supersample = 4

type rgba struct {
	r, g, b, a float64
}

func rgb(r, g, b uint8) rgba {
	return rgba{float64(r) / 255, float64(g) / 255, float64(b) / 255, 1}
}

var (
	bgTop    = rgb(0x17, 0x53, 0x9b)
	bgBottom = rgb(0x10, 0x3c, 0x74)
	white    = rgb(0xff, 0xff, 0xff)
	okCol    = rgb(0x2e, 0xcc, 0x71)
	warnCol  = rgb(0xf3, 0x9c, 0x12)
	errCol   = rgb(0xe7, 0x4c, 0x3c)
	unkCol   = rgb(0x8e, 0x9a, 0xa8)
)

type canvas struct {
	w, h int
	pix  []rgba
}

func newCanvas(w, h int) *canvas {
	return &canvas{w: w, h: h, pix: make([]rgba, w*h)}
}

func (c *canvas) blend(x, y int, src rgba, coverage float64) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h || coverage <= 0 {
		return
	}
	a := src.a * coverage
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	i := y*c.w + x
	dst := &c.pix[i]
	outA := a + dst.a*(1-a)
	if outA <= 0 {
		return
	}
	dst.r = (src.r*a + dst.r*dst.a*(1-a)) / outA
	dst.g = (src.g*a + dst.g*dst.a*(1-a)) / outA
	dst.b = (src.b*a + dst.b*dst.a*(1-a)) / outA
	dst.a = outA
}

// fillRoundRect draws an axis aligned rounded rectangle.
func (c *canvas) fillRoundRect(x0, y0, x1, y1, r float64, col rgba) {
	for y := int(math.Floor(y0)); y <= int(math.Ceil(y1)); y++ {
		for x := int(math.Floor(x0)); x <= int(math.Ceil(x1)); x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			if insideRoundRect(px, py, x0, y0, x1, y1, r) {
				c.blend(x, y, col, 1)
			}
		}
	}
}

func insideRoundRect(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx := math.Min(math.Max(px, x0+r), x1-r)
	cy := math.Min(math.Max(py, y0+r), y1-r)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

// fillCircle draws a filled circle.
func (c *canvas) fillCircle(cx, cy, r float64, col rgba) {
	for y := int(math.Floor(cy - r)); y <= int(math.Ceil(cy+r)); y++ {
		for x := int(math.Floor(cx - r)); x <= int(math.Ceil(cx+r)); x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			if dx*dx+dy*dy <= r*r {
				c.blend(x, y, col, 1)
			}
		}
	}
}

// drawLine draws a thick line segment.
func (c *canvas) drawLine(x0, y0, x1, y1, width float64, col rgba) {
	half := width / 2
	minX := math.Min(x0, x1) - half
	maxX := math.Max(x0, x1) + half
	minY := math.Min(y0, y1) - half
	maxY := math.Max(y0, y1) + half
	for y := int(math.Floor(minY)); y <= int(math.Ceil(maxY)); y++ {
		for x := int(math.Floor(minX)); x <= int(math.Ceil(maxX)); x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			if distToSegment(px, py, x0, y0, x1, y1) <= half {
				c.blend(x, y, col, 1)
			}
		}
	}
}

func distToSegment(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	lenSq := dx*dx + dy*dy
	if lenSq == 0 {
		return math.Hypot(px-x0, py-y0)
	}
	t := ((px-x0)*dx + (py-y0)*dy) / lenSq
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
}

// gradientBackground fills the rounded square background.
func (c *canvas) gradientBackground(x0, y0, x1, y1, r float64) {
	for y := int(math.Floor(y0)); y <= int(math.Ceil(y1)); y++ {
		t := (float64(y) - y0) / (y1 - y0)
		col := rgba{
			r: bgTop.r + (bgBottom.r-bgTop.r)*t,
			g: bgTop.g + (bgBottom.g-bgTop.g)*t,
			b: bgTop.b + (bgBottom.b-bgTop.b)*t,
			a: 1,
		}
		for x := int(math.Floor(x0)); x <= int(math.Ceil(x1)); x++ {
			if insideRoundRect(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, r) {
				c.blend(x, y, col, 1)
			}
		}
	}
}

// envelope draws a white envelope in the middle of the canvas.
func (c *canvas) envelope(size float64, bg rgba) {
	// Envelope body.
	x0, y0 := 0.20*size, 0.28*size
	x1, y1 := 0.80*size, 0.72*size
	c.fillRoundRect(x0, y0, x1, y1, 0.04*size, white)
	// Flap.
	c.drawLine(x0+0.02*size, y0+0.03*size, size/2, size*0.53, 0.055*size, bg)
	c.drawLine(size/2, size*0.53, x1-0.02*size, y0+0.03*size, 0.055*size, bg)
}

// downsample averages every supersample x supersample block.
func (c *canvas) downsample(factor int) *image.NRGBA {
	w, h := c.w/factor, c.h/factor
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a float64
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					p := c.pix[(y*factor+dy)*c.w+(x*factor+dx)]
					r += p.r * p.a
					g += p.g * p.a
					b += p.b * p.a
					a += p.a
				}
			}
			n := float64(factor * factor)
			if a == 0 {
				out.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: clamp(r / a * 255),
				G: clamp(g / a * 255),
				B: clamp(b / a * 255),
				A: clamp(a / n * 255),
			})
		}
	}
	return out
}

func clamp(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}

func renderApp(size int) *image.NRGBA {
	c := newCanvas(size*supersample, size*supersample)
	s := float64(size * supersample)
	c.gradientBackground(0, 0, s, s, 0.22*s)
	c.envelope(s, bgBottom)
	return c.downsample(supersample)
}

// renderTray draws the tray icon: the application tile with a coloured status
// dot in the lower right corner, so it stays readable on light and dark
// taskbars alike.
func renderTray(size int, status rgba) *image.NRGBA {
	c := newCanvas(size*supersample, size*supersample)
	s := float64(size * supersample)
	c.gradientBackground(0, 0, s, s, 0.24*s)
	c.envelope(s*0.96, bgTop)
	// Status dot with a light ring.
	cx, cy := 0.755*s, 0.755*s
	c.fillCircle(cx, cy, 0.30*s, white)
	c.fillCircle(cx, cy, 0.23*s, status)
	return c.downsample(supersample)
}

func writePNG(path string, img image.Image) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// writeICO packs PNG images into a Windows .ico file (Vista+ format).
func writeICO(path string, images []*image.NRGBA) error {
	type entry struct {
		width, height int
		data          []byte
	}
	var entries []entry
	for _, img := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		entries = append(entries, entry{width: img.Bounds().Dx(), height: img.Bounds().Dy(), data: buf.Bytes()})
	}
	var out bytes.Buffer
	// ICONDIR
	_ = binary.Write(&out, binary.LittleEndian, uint16(0))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w, h := byte(e.width), byte(e.height)
		if e.width >= 256 {
			w, h = 0, 0
		}
		out.WriteByte(w)
		out.WriteByte(h)
		out.WriteByte(0)                                        // palette
		out.WriteByte(0)                                        // reserved
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&out, binary.LittleEndian, uint16(32)) // bpp
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		_ = binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func main() {
	uiDir := filepath.Join("internal", "ui", "assets")
	appSizes := []int{16, 32, 48, 64, 128, 256}
	var icoImages []*image.NRGBA
	for _, size := range appSizes {
		img := renderApp(size)
		icoImages = append(icoImages, img)
		path := filepath.Join(uiDir, fmt.Sprintf("app-%d.png", size))
		if err := writePNG(path, img); err != nil {
			log.Fatalf("не удалось записать %s: %v", path, err)
		}
	}
	if err := writeICO(filepath.Join("assets", "app.ico"), icoImages); err != nil {
		log.Fatalf("не удалось записать иконку: %v", err)
	}

	statuses := map[string]rgba{
		"ok":      okCol,
		"warning": warnCol,
		"error":   errCol,
		"unknown": unkCol,
	}
	for name, col := range statuses {
		for _, size := range []int{16, 32, 64} {
			img := renderTray(size, col)
			if err := writePNG(filepath.Join(uiDir, fmt.Sprintf("tray-%s-%d.png", name, size)), img); err != nil {
				log.Fatalf("не удалось записать иконку: %v", err)
			}
		}
	}
	fmt.Println("иконки созданы")
}
