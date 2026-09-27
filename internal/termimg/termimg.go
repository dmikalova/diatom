// Package termimg shows images in the terminal with kitty's graphics protocol
// and its Unicode placeholders, which kitty and Ghostty support. An image is
// sent to the terminal once, out of band, placed virtually over a box of
// cells; the window then draws placeholder cells where it goes, so the image
// lives in the cell grid like any text, and a cell renderer needs to know
// nothing of it.
package termimg

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // GIF, for image.Decode
	_ "image/jpeg" // JPEG, for image.Decode
	"image/png"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/bmp"  // BMP, for image.Decode
	_ "golang.org/x/image/webp" // WebP, for image.Decode
)

// svgSize is how many pixels an SVG is drawn at on its longer side.
const svgSize = 512

// Supported reports whether the file at path is an image termimg shows, by
// its extension.
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	}
	return false
}

// Enabled reports whether the terminal diatom runs in shows images this way,
// from its environment.
func Enabled(getenv func(string) string) bool {
	return getenv("TERM_PROGRAM") == "ghostty" || getenv("KITTY_WINDOW_ID") != "" ||
		strings.Contains(getenv("TERM"), "kitty") || strings.Contains(getenv("TERM"), "ghostty")
}

// Decode reads an image file by its extension, drawing an SVG.
func Decode(path string, data []byte) (image.Image, error) {
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		return drawSVG(data)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// drawSVG rasterizes an SVG at svgSize pixels on its longer side.
func drawSVG(data []byte) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, fmt.Errorf("svg: %w", err)
	}
	vw, vh := icon.ViewBox.W, icon.ViewBox.H
	if vw <= 0 || vh <= 0 {
		vw, vh = 1, 1
	}
	w, h := svgSize, svgSize
	if vw > vh {
		h = max(int(float64(svgSize)*vh/vw), 1)
	} else {
		w = max(int(float64(svgSize)*vw/vh), 1)
	}
	icon.SetTarget(0, 0, float64(w), float64(h))
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	scanner := rasterx.NewScannerGV(w, h, rgba, rgba.Bounds())
	icon.Draw(rasterx.NewDasher(w, h, scanner), 1)
	return rgba, nil
}

// Box fits an image of w by h pixels into at most cols by rows cells, a cell
// being about twice as tall as it is wide, and never more rows than the
// placeholders can number.
func Box(w, h, cols, rows int) (c, r int) {
	rows = min(rows, len(diacritics))
	if w <= 0 || h <= 0 || cols <= 0 || rows <= 0 {
		return 0, 0
	}
	r = rows
	c = max(int(float64(r)*2*float64(w)/float64(h)+0.5), 1)
	if c > cols {
		c = cols
		r = max(int(float64(c)*float64(h)/(2*float64(w))+0.5), 1)
	}
	return c, r
}

// chunk is the most base64 one of the protocol's escape sequences may carry.
const chunk = 4096

// Transmit is what sends img to the terminal as image id, placed virtually
// over cols by rows cells, for Placeholder to show.
func Transmit(id uint32, img image.Image, cols, rows int) (string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	var b strings.Builder
	first := fmt.Sprintf("a=T,U=1,f=100,t=d,q=2,i=%d,c=%d,r=%d", id, cols, rows)
	for len(data) > 0 {
		n := min(chunk, len(data))
		more := 0
		if n < len(data) {
			more = 1
		}
		keys := fmt.Sprintf("m=%d", more)
		if first != "" {
			keys, first = first+","+keys, ""
		}
		fmt.Fprintf(&b, "\x1b_G%s;%s\x1b\\", keys, data[:n])
		data = data[n:]
	}
	return b.String(), nil
}

// lastID is the last image number handed out: every image the window sends
// needs its own.
var lastID atomic.Uint32

// NextID is a number for a new image, unique in the process and within the
// 24 bits a placeholder's color holds.
func NextID() uint32 { return lastID.Add(1) & 0xffffff }

// Sent reports whether any image has been given a number to send.
func Sent() bool { return lastID.Load() > 0 }

// DeleteAll is what frees every image the terminal holds for the window.
const DeleteAll = "\x1b_Ga=d,d=A,q=2\x1b\\"

// placeholder is the character whose cells show an image.
const placeholder = '\U0010EEEE'

// Placeholder is the lines of cells that show image id over cols by rows
// cells: the image's number is their color, and the first cell of each line
// is marked with its row and column, the rest following on from it.
func Placeholder(id uint32, cols, rows int) []string {
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	rest := strings.Repeat(string(placeholder), max(cols-1, 0))
	lines := make([]string, 0, rows)
	for r := range min(rows, len(diacritics)) {
		lines = append(lines, color+string(placeholder)+string(diacritics[r])+
			string(diacritics[0])+rest+"\x1b[39m")
	}
	return lines
}
