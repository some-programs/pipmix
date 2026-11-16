package pipmix

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	helpSectionStyle = lipgloss.NewStyle().Bold(true)
	helpRuleStyle    = ansi.Style{}.Underline(true).UnderlineColor(ansi.BrightBlack)
)

const helpGap = 3

type helpLine struct {
	text string
	rule bool // underlined separator at the end of a binding
}

// helpLines formats the current tab's help into columns fitting width and height.
func (m *mixer) helpLines(width, height int) []string {
	var blocks [][]helpLine
	for _, s := range m.tabSections() {
		blocks = append(blocks, helpBlock(s, width))
	}
	columns := arrangeColumns(blocks, width, height)
	lines := make([]string, tallest(columns))
	for i := range lines {
		var b strings.Builder
		for c, col := range columns {
			if c > 0 {
				b.WriteString(strings.Repeat(" ", helpGap))
			}
			var line helpLine
			if i < len(col) {
				line = col[i]
			}
			text := fit(line.text, columnWidth(col))
			if line.rule {
				text = helpRuleStyle.Styled(text)
			}
			b.WriteString(text)
		}
		lines[i] = fit(b.String(), width)
	}
	return lines
}

// scrollHelp scrolls the help by delta lines, within its length.
func (m *mixer) scrollHelp(delta int) {
	h := m.listHeight()
	n := len(m.helpLines(m.width, h))
	m.helpOff = max(0, min(m.helpOff+delta, n-h))
}

// helpView renders the visible lines of the help.
func (m *mixer) helpView(width, height int) []string {
	lines := m.helpLines(width, height)
	end := min(len(lines), m.helpOff+height)
	return lines[min(m.helpOff, end):end]
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
	lines := []helpLine{{text: helpSectionStyle.Render(ansi.Truncate(s.title, width, "…"))}}
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
			lines = append(lines, helpLine{ansi.Truncate(line, width, "…"), r == rows-1})
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

// fit truncates or pads s to width cells.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
