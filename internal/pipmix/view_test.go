package pipmix

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/tui"
)

// drawGrid draws m into a new grid.
func drawGrid(m *mixer) *tui.Grid {
	g := tui.NewGrid(m.width, m.height)
	m.draw(g)
	return g
}

// find returns the cell position of the first text on g.
func find(g *tui.Grid, text string) (x, y int, ok bool) {
	w, h := g.Size()
	for y := range h {
		var row strings.Builder
		var cols []int // the column of each byte of row
		for x := range w {
			c := g.At(x, y)
			row.WriteString(c.Content)
			for range len(c.Content) {
				cols = append(cols, x)
			}
		}
		if i := strings.Index(row.String(), text); i >= 0 {
			return cols[i], y, true
		}
	}
	return 0, 0, false
}

// styleOf returns the style of the first cell of the first text on g.
func styleOf(g *tui.Grid, text string) (tui.Style, bool) {
	x, y, ok := find(g, text)
	if !ok {
		return tui.Style{}, false
	}
	return g.At(x, y).Style, true
}

// lines returns the rows of g as plain text.
func lines(g *tui.Grid) []string {
	return strings.Split(ansi.Strip(g.String()), "\n")
}

// penText returns the plain text that draw writes into a pen of width
// cells.
func penText(width int, draw func(p *tui.Pen)) string {
	g := tui.NewGrid(width, 1)
	p := g.Pen(0, 0, width)
	draw(&p)
	return strings.TrimRight(ansi.Strip(g.String()), " ")
}
