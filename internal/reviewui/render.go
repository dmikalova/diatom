package reviewui

import (
	"slices"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/dmikalova/diatom/internal/review"
	"github.com/dmikalova/diatom/internal/tui"
)

// The reviewer colors with the terminal's own 16 ANSI colors (package tui),
// so it follows the terminal's theme.
const (
	noColor = tui.NoColor
	red     = tui.Red
	green   = tui.Green
	yellow  = tui.Yellow
	magenta = tui.Magenta
	cyan    = tui.Cyan
	gray    = tui.Gray
	reset   = tui.Reset
)

var (
	sgr    = tui.SGR
	fgCode = tui.FG
)

// rgb is a 24-bit color.
type rgb [3]int

// bg is the SGR codes of c as the background.
func (c rgb) bg() []int { return []int{48, 2, c[0], c[1], c[2]} }

// shades are the backgrounds of added and deleted lines, and of the words
// changed on them, after delta's: tinted enough to tell apart, and never so
// strong that the syntax colors drawn over them stop reading. These are the
// only colors the reviewer doesn't take from the terminal's palette, since
// the 16 colors have no pale greens and reds; each has a light and a dark
// set, for the terminal's background.
type shades struct{ add, addWord, del, delWord rgb }

var (
	lightShades = shades{
		add: rgb{0xd0, 0xff, 0xd0}, addWord: rgb{0xa0, 0xef, 0xa0},
		del: rgb{0xff, 0xe0, 0xe0}, delWord: rgb{0xff, 0xc0, 0xc0},
	}
	darkShades = shades{
		add: rgb{0x00, 0x28, 0x00}, addWord: rgb{0x00, 0x60, 0x00},
		del: rgb{0x3f, 0x00, 0x01}, delWord: rgb{0x90, 0x10, 0x11},
	}
	// shade is the set in use: light until the terminal says it is dark.
	shade = lightShades
)

// SetDark picks the shades for a dark background, or a light one.
func SetDark(dark bool) {
	shade = lightShades
	if dark {
		shade = darkShades
	}
}

// tokenColor maps a syntax token to a color, after Monokai: red keywords,
// amber strings, green functions, purple constants, cyan types, gray
// comments.
func tokenColor(t chroma.TokenType) int {
	switch {
	case t.InCategory(chroma.Comment):
		return gray
	case t == chroma.KeywordType || t == chroma.NameClass || t == chroma.NameBuiltin:
		return cyan
	case t == chroma.KeywordConstant || t.InSubCategory(chroma.LiteralNumber) || t == chroma.NameConstant:
		return magenta
	case t.InCategory(chroma.Keyword):
		return red
	case t.InSubCategory(chroma.LiteralString):
		return yellow
	case t == chroma.NameFunction:
		return green
	}
	return noColor
}

// cell is one rune of a rendered line.
type cell struct {
	r  rune
	fg int
	// changed marks a rune the word-level diff found changed.
	changed bool
}

// line is one line of a hunk, ready to render.
type line struct {
	op         gitdiff.LineOp
	oldN, newN int
	cells      []cell
}

// lines renders a hunk's lines with syntax colors and word-level changes.
// Each side of the hunk is highlighted as one text, so a string or comment
// that spans lines keeps its color.
func lines(h review.Hunk) []line {
	if h.Binary() {
		return nil
	}
	frag := h.Fragment
	var oldText, newText []string
	oldIdx := make([]int, len(frag.Lines))
	newIdx := make([]int, len(frag.Lines))
	for i, l := range frag.Lines {
		text := strings.TrimSuffix(l.Line, "\n")
		oldIdx[i], newIdx[i] = -1, -1
		if l.Op != gitdiff.OpAdd {
			oldIdx[i] = len(oldText)
			oldText = append(oldText, text)
		}
		if l.Op != gitdiff.OpDelete {
			newIdx[i] = len(newText)
			newText = append(newText, text)
		}
	}
	oldCells, newCells := highlight(h.OldPath, oldText), highlight(h.Path, newText)

	out := make([]line, len(frag.Lines))
	oldN, newN := int(frag.OldPosition), int(frag.NewPosition)
	for i, l := range frag.Lines {
		ln := line{op: l.Op}
		switch l.Op {
		case gitdiff.OpDelete:
			ln.oldN, ln.cells = oldN, oldCells[oldIdx[i]]
			oldN++
		case gitdiff.OpAdd:
			ln.newN, ln.cells = newN, newCells[newIdx[i]]
			newN++
		default:
			ln.oldN, ln.newN, ln.cells = oldN, newN, newCells[newIdx[i]]
			oldN++
			newN++
		}
		out[i] = ln
	}
	pairChanges(out)
	return out
}

// pairChanges runs the word-level diff over each run of deleted lines and
// the run of added lines right after it, pairing them in order.
func pairChanges(ls []line) {
	for i := 0; i < len(ls); {
		if ls[i].op != gitdiff.OpDelete {
			i++
			continue
		}
		dels := i
		for i < len(ls) && ls[i].op == gitdiff.OpDelete {
			i++
		}
		adds := i
		for i < len(ls) && ls[i].op == gitdiff.OpAdd {
			i++
		}
		for k := 0; dels+k < adds && adds+k < i; k++ {
			wordDiff(ls[dels+k].cells, ls[adds+k].cells)
		}
	}
}

// highlight colors lines of text as source for path's language.
func highlight(path string, text []string) [][]cell {
	out := make([][]cell, len(text))
	lexer := lexers.Match(path)
	if lexer == nil {
		for i, t := range text {
			out[i] = plain(t)
		}
		return out
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, strings.Join(text, "\n")+"\n")
	if err != nil {
		for i, t := range text {
			out[i] = plain(t)
		}
		return out
	}
	row := 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		fg := tokenColor(tok.Type)
		for _, r := range tok.Value {
			if r == '\n' {
				row++
				continue
			}
			if row < len(out) {
				out[row] = append(out[row], cell{r: r, fg: fg})
			}
		}
	}
	return out
}

func plain(s string) []cell {
	cells := make([]cell, 0, len(s))
	for _, r := range s {
		cells = append(cells, cell{r: r, fg: noColor})
	}
	return cells
}

// maxDiffWork bounds the word diff's table, so a very long line is marked
// changed as a whole rather than stalling the reviewer.
const maxDiffWork = 250_000

// wordDiff marks the words that differ between an old and a new line.
func wordDiff(oldCells, newCells []cell) {
	a, b := words(oldCells), words(newCells)
	if len(a)*len(b) > maxDiffWork {
		markAll(oldCells)
		markAll(newCells)
		return
	}
	// lcs[i][j] is the longest common run of words of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i, v := range slices.Backward(a) {
		for j := len(b) - 1; j >= 0; j-- {
			if v.text == b[j].text {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i].text == b[j].text:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			mark(oldCells, a[i])
			i++
		default:
			mark(newCells, b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		mark(oldCells, a[i])
	}
	for ; j < len(b); j++ {
		mark(newCells, b[j])
	}
}

type word struct {
	text       string
	start, end int
}

// words splits a line into words: runs of letters, digits and underscores,
// runs of spaces, and single other runes.
func words(cells []cell) []word {
	var out []word
	class := func(r rune) int {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	for i := 0; i < len(cells); {
		j := i + 1
		if c := class(cells[i].r); c != 0 {
			for j < len(cells) && class(cells[j].r) == c {
				j++
			}
		}
		var b strings.Builder
		for _, c := range cells[i:j] {
			b.WriteRune(c.r)
		}
		out = append(out, word{b.String(), i, j})
		i = j
	}
	return out
}

func mark(cells []cell, w word) {
	for k := w.start; k < w.end; k++ {
		cells[k].changed = true
	}
}

func markAll(cells []cell) {
	for k := range cells {
		cells[k].changed = true
	}
}

// tabWidth is how many columns a tab takes.
const tabWidth = 4

// renderCells renders a line's content in rows of at most width columns,
// wrapping a long line at a space where it can. An added or deleted line is
// tinted green or red across the whole width, its changed words a shade
// stronger, and its syntax colors kept.
func renderCells(cells []cell, op gitdiff.LineOp, width int) []string {
	cells = expandTabs(cells)
	width = max(width, 1)
	var rows []string
	for len(cells) > width {
		cut := width
		for i := width - 1; i > 0; i-- {
			if cells[i].r == ' ' {
				cut = i + 1
				break
			}
		}
		rows = append(rows, paint(cells[:cut], op, width))
		cells = cells[cut:]
	}
	return append(rows, paint(cells, op, width))
}

// expandTabs turns each tab into the spaces it takes, keeping its colors.
func expandTabs(cells []cell) []cell {
	out := make([]cell, 0, len(cells))
	for _, c := range cells {
		if c.r != '\t' {
			out = append(out, c)
			continue
		}
		c.r = ' '
		for range tabWidth - len(out)%tabWidth {
			out = append(out, c)
		}
	}
	return out
}

// paint renders cells with their syntax colors, on the tint of an added or
// deleted line, filled out to width.
func paint(cells []cell, op gitdiff.LineOp, width int) string {
	var line, word []int
	switch op {
	case gitdiff.OpAdd:
		line, word = shade.add.bg(), shade.addWord.bg()
	case gitdiff.OpDelete:
		line, word = shade.del.bg(), shade.delWord.bg()
	}
	if len(line) > 0 {
		// Clipped, so filling it out never writes over the row after it.
		cells = slices.Clip(cells)
		for range width - len(cells) {
			cells = append(cells, cell{r: ' ', fg: noColor})
		}
	}
	var b strings.Builder
	state := ""
	for _, c := range cells {
		codes := line
		if c.changed && len(word) > 0 {
			codes = word
		}
		if c.fg != noColor {
			codes = append(slices.Clone(codes), fgCode(c.fg))
		}
		want := reset
		if len(codes) > 0 {
			want = reset + sgr(codes...)
		}
		if want != state {
			b.WriteString(want)
			state = want
		}
		b.WriteRune(c.r)
	}
	b.WriteString(reset)
	return b.String()
}
