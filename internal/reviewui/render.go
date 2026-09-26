package reviewui

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/dmikalova/diatom/internal/review"
)

// The reviewer colors with the terminal's own 16 ANSI colors rather than a
// fixed palette, so it follows the terminal's theme.
const (
	noColor = -1
	red     = 1
	green   = 2
	yellow  = 3
	blue    = 4
	magenta = 5
	cyan    = 6
	gray    = 8
)

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

// sgr returns an ANSI select-graphic-rendition sequence.
func sgr(codes ...int) string {
	s := make([]string, len(codes))
	for i, c := range codes {
		s[i] = strconv.Itoa(c)
	}
	return "\x1b[" + strings.Join(s, ";") + "m"
}

const reset = "\x1b[0m"

// fgCode is the SGR code for one of the 16 colors as foreground.
func fgCode(c int) int {
	if c >= 8 {
		return 90 + c - 8
	}
	return 30 + c
}

// bgCode is the SGR code for one of the 16 colors as background.
func bgCode(c int) int {
	if c >= 8 {
		return 100 + c - 8
	}
	return 40 + c
}

// tabWidth is how many columns a tab takes.
const tabWidth = 4

// renderCells renders a line's content into at most width columns. Deleted
// lines are red throughout, so they read as gone; the changed words of a
// changed line stand out on their side's color.
func renderCells(cells []cell, op gitdiff.LineOp, width int) string {
	var b strings.Builder
	col := 0
	state := ""
	for _, c := range cells {
		if col >= width {
			break
		}
		fg := c.fg
		if op == gitdiff.OpDelete {
			fg = red
		}
		var codes []int
		if c.changed {
			bg := green
			if op == gitdiff.OpDelete {
				bg = red
			}
			codes = append(codes, bgCode(bg), fgCode(0))
		} else if fg != noColor {
			codes = append(codes, fgCode(fg))
		}
		want := reset
		if len(codes) > 0 {
			want = reset + sgr(codes...)
		}
		if want != state {
			b.WriteString(want)
			state = want
		}
		if c.r == '\t' {
			n := min(tabWidth-col%tabWidth, width-col)
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(c.r)
		col++
	}
	b.WriteString(reset)
	return b.String()
}
