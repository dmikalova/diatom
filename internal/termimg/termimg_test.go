package termimg

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 12">
  <rect width="24" height="12" fill="#f0b429"/></svg>`

func TestDecode(t *testing.T) {
	img, err := Decode("gem.SVG", []byte(icon))
	if err != nil || img.Bounds().Dx() != svgSize || img.Bounds().Dy() != svgSize/2 {
		t.Fatalf("svg = %v, %v", img.Bounds(), err)
	}
	if r, g, b, _ := img.At(10, 10).RGBA(); r>>8 != 0xf0 || g>>8 != 0xb4 || b>>8 != 0x29 {
		t.Errorf("svg color = %x %x %x", r>>8, g>>8, b>>8)
	}
	tall, err := Decode("tall.svg", []byte(strings.Replace(icon, "0 0 24 12", "0 0 12 24", 1)))
	if err != nil || tall.Bounds().Dx() != svgSize/2 {
		t.Errorf("tall svg = %v, %v", tall.Bounds(), err)
	}
	if img, err := Decode(
		"none.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	); err != nil ||
		img.Bounds().Dx() != svgSize {
		t.Errorf("no view box = %v, %v", img, err)
	}
	if _, err := Decode("bad.svg", []byte("<svg")); err == nil {
		t.Error("a broken svg decoded")
	}
	var buf bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(1, 1, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if img, err := Decode("dot.png", buf.Bytes()); err != nil || img.Bounds().Dx() != 3 {
		t.Errorf("png = %v, %v", img, err)
	}
	if _, err := Decode(
		"bad.png",
		[]byte("nope"),
	); err == nil ||
		!strings.Contains(err.Error(), "bad.png") {
		t.Errorf("a broken png = %v", err)
	}
}

func TestSupportedAndEnabled(t *testing.T) {
	for path, want := range map[string]bool{"a.PNG": true, "b.svg": true, "c.webp": true, "d.go": false} {
		if Supported(path) != want {
			t.Errorf("Supported(%s) = %v", path, !want)
		}
	}
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	if !Enabled(env("TERM_PROGRAM", "ghostty")) || !Enabled(env("TERM", "xterm-kitty")) ||
		!Enabled(env("KITTY_WINDOW_ID", "1")) || Enabled(env("TERM", "xterm-256color")) {
		t.Error("Enabled reads the terminal wrong")
	}
}

func TestBox(t *testing.T) {
	for _, c := range []struct{ w, h, cols, rows, wantC, wantR int }{
		{100, 100, 40, 10, 20, 10}, // square: twice as many columns as rows
		{400, 100, 40, 10, 40, 5},  // wide: held to the columns
		{0, 100, 40, 10, 0, 0},
		{100, 100, 40, 1000, 40, 20},
	} {
		if gotC, gotR := Box(c.w, c.h, c.cols, c.rows); gotC != c.wantC || gotR != c.wantR {
			t.Errorf("Box(%d, %d, %d, %d) = %d, %d", c.w, c.h, c.cols, c.rows, gotC, gotR)
		}
	}
}

func TestTransmitAndPlaceholder(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for x := range 300 {
		for y := range 300 {
			// Noise, so the image takes several chunks.
			n := uint32(x*7919+y*104729) * 2654435761
			img.Set(
				x,
				y,
				color.RGBA{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: 255},
			)
		}
	}
	seq, err := Transmit(7, img, 20, 10)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(seq, "\x1b\\")
	if len(parts) < 3 ||
		!strings.HasPrefix(parts[0], "\x1b_Ga=T,U=1,f=100,t=d,q=2,i=7,c=20,r=10,m=1;") ||
		!strings.HasPrefix(parts[1], "\x1b_Gm=") ||
		!strings.HasPrefix(parts[len(parts)-2], "\x1b_Gm=0;") {
		t.Errorf("chunks = %d, %q…", len(parts), seq[:60])
	}
	lines := Placeholder(0x010203, 5, 3)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "\x1b[38;2;1;2;3m\U0010EEEE") ||
		!strings.ContainsRune(lines[2], diacritics[2]) || ansi.StringWidth(lines[1]) != 5 {
		t.Errorf("placeholder = %q", lines)
	}
	if a, b := NextID(), NextID(); a == b || !Sent() || !strings.Contains(DeleteAll, "a=d") {
		t.Error("ids repeat")
	}
}
