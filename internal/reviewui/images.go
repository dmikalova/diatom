package reviewui

import (
	"fmt"
	"image"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/termimg"
)

// previewRows is the most rows an image's preview takes, and previewGap the
// columns between the before and the after.
const (
	previewRows = 10
	previewGap  = 4
)

// picture is one side of an image's change, as sent to the terminal.
type picture struct {
	id         uint32
	cols, rows int
}

// images are the pictures the reviewer has drawn: the ones sent to the
// terminal by what they show and how big, the images read, by commit and
// path, and the sequences still to send.
type images struct {
	on      bool
	sent    map[string]picture
	decoded map[string]decoded
	pending strings.Builder
}

// decoded is an image file as it was at a commit, or why it can't be shown.
type decoded struct {
	img image.Image
	err error
}

// Images is what to send the terminal before the reviewer's next frame: the
// images its hunk on screen shows, sent once each. The window writes it
// out of band, since the frame itself holds only their placeholders.
func (m *Model) Images() string {
	if m.cur >= 0 {
		m.preview(m.items[m.cur])
	}
	out := m.pics.pending.String()
	m.pics.pending.Reset()
	return out
}

// preview shows an image's hunk as the image was before the commit and as
// it is after, side by side, or nothing for a hunk of any other file.
func (m *Model) preview(it review.Item) []string {
	if !m.pics.on || m.combined || !termimg.Supported(it.Path) {
		return nil
	}
	half := max((max(m.width, 20)-previewGap)/2, 4)
	rows := min(previewRows, max(m.height/3, 3))
	sides := []struct {
		label, rev, path string
		gone             bool
	}{
		{"before", it.Commit + "^", it.OldPath, it.New},
		{"after", it.Commit, it.Path, it.Deleted},
	}
	var cells [2][]string
	var labels [2]string
	height := 0
	for i, s := range sides {
		labels[i] = s.label
		if s.gone {
			labels[i] = map[bool]string{true: "none: the file is new", false: "none: the file is gone"}[i == 0]
			continue
		}
		d := m.decode(s.rev, s.path)
		if d.err != nil {
			labels[i] = s.label + ": can't show it, " + d.err.Error()
			continue
		}
		b := d.img.Bounds()
		c, r := termimg.Box(b.Dx(), b.Dy(), half, rows)
		cells[i] = m.picture(s.rev+":"+s.path, d.img, c, r)
		labels[i] = fmt.Sprintf("%s · %d×%d", s.label, b.Dx(), b.Dy())
		height = max(height, r)
	}
	pad := func(s string) string { return s + strings.Repeat(" ", max(half-ansi.StringWidth(s), 0)) }
	gap := strings.Repeat(" ", previewGap)
	lines := []string{dim(pad(ansi.Truncate(labels[0], half, "…")) + gap +
		ansi.Truncate(labels[1], half, "…"))}
	for r := range height {
		var row [2]string
		for i := range 2 {
			if r < len(cells[i]) {
				row[i] = cells[i][r]
			}
		}
		lines = append(lines, pad(row[0])+gap+row[1])
	}
	return append(lines, "")
}

// decode reads an image file as it was at rev, once.
func (m *Model) decode(rev, path string) decoded {
	key := rev + ":" + path
	if d, ok := m.pics.decoded[key]; ok {
		return d
	}
	var d decoded
	data, err := m.repo.Output(m.ctx, "show", key)
	if err == nil {
		d.img, d.err = termimg.Decode(path, []byte(data))
	} else {
		d.err = fmt.Errorf("git has no %s", key)
	}
	m.pics.decoded[key] = d
	return d
}

// picture is the placeholder lines of an image drawn over cols by rows
// cells, sending it to the terminal the first time it is drawn that big.
func (m *Model) picture(key string, img image.Image, cols, rows int) []string {
	key = fmt.Sprintf("%s@%dx%d", key, cols, rows)
	p, ok := m.pics.sent[key]
	if !ok {
		p = picture{id: termimg.NextID(), cols: cols, rows: rows}
		seq, err := termimg.Transmit(p.id, img, cols, rows)
		if err != nil {
			return nil
		}
		m.pics.pending.WriteString(seq)
		m.pics.sent[key] = p
	}
	return termimg.Placeholder(p.id, p.cols, p.rows)
}
