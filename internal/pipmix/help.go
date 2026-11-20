package pipmix

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/tui"
)

var (
	helpSectionStyle = tui.Style{Attrs: tui.Bold}
	helpRuleStyle    = tui.Style{Attrs: tui.Underline, UnderlineColor: tui.BrightBlack}
)

const helpGap = 3

type helpLine struct {
	text  string
	style tui.Style
	rule  bool // underlined separator at the end of a binding
}

// helpColumns arranges the current tab's help into columns fitting width
// and height.
func (m *mixer) helpColumns(width, height int) [][]helpLine {
	var blocks [][]helpLine
	for _, s := range m.tabSections() {
		blocks = append(blocks, helpBlock(s, width))
	}
	return arrangeColumns(blocks, width, height)
}

// scrollHelp scrolls the help by delta lines, within its length.
func (m *mixer) scrollHelp(delta int) {
	h := m.listHeight()
	n := tallest(m.helpColumns(m.width, h))
	m.helpOff = max(0, min(m.helpOff+delta, n-h))
}

// drawHelp draws the visible lines of the help into the h rows of the list.
func (m *mixer) drawHelp(g *tui.Grid, h int) {
	columns := m.helpColumns(m.width, h)
	widths := make([]int, len(columns))
	for c, col := range columns {
		widths[c] = columnWidth(col)
	}
	for i := m.helpOff; i < min(tallest(columns), m.helpOff+h); i++ {
		p := g.Pen(0, listTop+i-m.helpOff, m.width)
		for c, col := range columns {
			if c > 0 {
				p.Repeat(" ", helpGap, plain)
			}
			var line helpLine
			if i < len(col) {
				line = col[i]
			}
			st, pad := line.style, plain
			if line.rule {
				st, pad = helpRuleStyle, helpRuleStyle
			}
			p.Fit(line.text, widths[c], st, pad)
		}
	}
}

func tallest(columns [][]helpLine) int {
	n := 0
	for _, c := range columns {
		n = max(n, len(c))
	}
	return n
}

func columnWidth(col []helpLine) int {
	w := 0
	for _, line := range col {
		w = max(w, ansi.StringWidth(line.text))
	}
	return w
}

func columnsWidth(columns [][]helpLine) int {
	w := (len(columns) - 1) * helpGap
	for _, c := range columns {
		w += columnWidth(c)
	}
	return w
}

// helpBlock renders a keybinding section to fit width.
func helpBlock(s section, width int) []helpLine {
	const indent = "  "
	keysWidth := 0
	for _, b := range s.bindings {
		for _, in := range b.inputs() {
			keysWidth = max(keysWidth, ansi.StringWidth(in))
		}
	}
	textIndent := len(indent) + keysWidth + 2
	lines := []helpLine{{text: ansi.Truncate(s.title, width, "…"), style: helpSectionStyle}}
	for _, b := range s.bindings {
		inputs := b.inputs()
		text := strings.Split(ansi.Wrap(b.help, max(1, width-textIndent), ""), "\n")
		rows := max(len(inputs), len(text))
		for r := range rows {
			var line string
			if r < len(inputs) {
				line = indent + inputs[r]
			}
			if r < len(text) {
				line += strings.Repeat(" ", textIndent-ansi.StringWidth(line)) + text[r]
			}
			lines = append(lines, helpLine{text: ansi.Truncate(line, width, "…"), rule: r == rows-1})
		}
	}
	return lines
}

func (b binding) inputs() []string {
	lines := []string{strings.Join(b.keys, ", ")}
	if b.mouse != "" {
		lines = append(lines, b.mouse)
	}
	return lines
}

// arrangeColumns packs blocks into the fewest columns that fit within width
// and height.
func arrangeColumns(blocks [][]helpLine, width, height int) [][]helpLine {
	if len(blocks) == 0 {
		return [][]helpLine{nil}
	}
	better := func(a, b [][]helpLine) bool {
		fitA, fitB := tallest(a) <= height, tallest(b) <= height
		switch {
		case fitA != fitB:
			return fitA
		case fitA && len(a) != len(b):
			return len(a) < len(b)
		case tallest(a) != tallest(b):
			return tallest(a) < tallest(b)
		}
		return len(a) < len(b)
	}
	var best [][]helpLine
	// Bit i of breaks starts a new column at block i+1.
	for breaks := range 1 << (len(blocks) - 1) {
		var columns [][]helpLine
		for i, b := range blocks {
			if i == 0 || breaks&(1<<(i-1)) != 0 {
				columns = append(columns, slices.Clone(b))
				continue
			}
			last := len(columns) - 1
			columns[last] = append(append(columns[last], helpLine{}), b...)
		}
		if best == nil || columnsWidth(columns) <= width && better(columns, best) {
			best = columns
		}
	}
	return best
}
