package pipmix

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/pw"
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

func TestItemHeightMatchesItemLines(t *testing.T) {
	m := testMixer()
	cases := []item{
		{id: 1, card: &pw.Card{ID: 1, Title: "Card"}},
		{id: 2, node: &pw.Node{ID: 2, Name: "novol"}},
		{id: 3, node: &pw.Node{ID: 3, Name: "mono", Channels: []pw.Channel{{Name: "MONO", Volume: 0.5}}}},
		{id: 4, node: &pw.Node{ID: 4, Name: "folded", Channels: stereo(0.5)}},
		{id: 5, node: &pw.Node{ID: 5, Name: "unfolded", Channels: []pw.Channel{{Name: "FL", Volume: 0.5}, {Name: "FR", Volume: 0.8}}}},
		{id: 6, node: &pw.Node{ID: 6, Name: "grp-leader", Channels: stereo(0.5)}, groupCount: 2, groupLeader: true},
		{id: 7, node: &pw.Node{ID: 7, Name: "grp-member", Channels: stereo(0.5)}, groupCount: 2, groupLeader: false},
		{id: 8, node: &pw.Node{ID: 8, Name: "grp-member-unfolded", Channels: []pw.Channel{{Name: "FL", Volume: 0.2}, {Name: "FR", Volume: 0.9}}}, groupCount: 2, groupLeader: false},
	}
	for _, it := range cases {
		if got, want := m.itemHeight(it), len(m.itemLines(it)); got != want {
			t.Errorf("itemHeight(%+v) = %d, want %d", it.id, got, want)
		}
	}
}
