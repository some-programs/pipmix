package tui

// Cell is one cell of a grid. A wide grapheme takes two cells: the left one
// holds it and has Width 2, and the right one is empty and has Width 0.
type Cell struct {
	Content string
	Width   uint8
	Style   Style
}

// Blank is an empty cell in the default style.
var Blank = Cell{Content: " ", Width: 1}

// Grid is a screen of cells. It remembers which rows changed since they
// were last flushed to a terminal.
type Grid struct {
	w, h  int
	cells []Cell
	dirty []bool
}

// NewGrid returns a blank grid of w×h cells.
func NewGrid(w, h int) *Grid {
	g := &Grid{}
	g.Resize(w, h)
	return g
}

// Resize makes g a blank grid of w×h cells.
func (g *Grid) Resize(w, h int) {
	w, h = max(w, 0), max(h, 0)
	g.w, g.h = w, h
	if cap(g.cells) >= w*h {
		g.cells = g.cells[:w*h]
	} else {
		g.cells = make([]Cell, w*h)
	}
	if cap(g.dirty) >= h {
		g.dirty = g.dirty[:h]
	} else {
		g.dirty = make([]bool, h)
	}
	g.Clear()
}

// Size returns the width and height of g.
func (g *Grid) Size() (w, h int) { return g.w, g.h }

// Clear blanks every cell.
func (g *Grid) Clear() {
	for i := range g.cells {
		g.cells[i] = Blank
	}
	for y := range g.dirty {
		g.dirty[y] = true
	}
}

// At returns the cell at x, y.
func (g *Grid) At(x, y int) Cell { return g.cells[y*g.w+x] }

func (g *Grid) row(y int) []Cell { return g.cells[y*g.w : (y+1)*g.w] }

// set puts c at x, y, blanking what remains of wide cells it overwrites
// in part. c must fit in the row.
func (g *Grid) set(x, y int, c Cell) {
	row := g.row(y)
	if row[x].Width == 0 && x > 0 {
		row[x-1] = Blank
	}
	if end := x + int(c.Width); row[end-1].Width == 2 && end < len(row) {
		row[end] = Blank
	}
	row[x] = c
	if c.Width == 2 {
		row[x+1] = Cell{Style: c.Style}
	}
	g.dirty[y] = true
}

// Pen returns a pen that writes row y from column x, clipping at column end.
func (g *Grid) Pen(x, y, end int) Pen {
	if y < 0 || y >= g.h {
		end = x
	}
	end = max(min(end, g.w), x)
	return Pen{g: g, y: y, x: x, start: x, end: end, want: end}
}

// String returns the rows of g as lines, with escape sequences for their
// styles.
func (g *Grid) String() string {
	var b []byte
	for y := range g.h {
		if y > 0 {
			b = append(b, '\n')
		}
		cur := Style{}
		for _, c := range g.row(y) {
			if c.Width == 0 {
				continue
			}
			if c.Style != cur {
				b = appendSGR(b, c.Style)
				cur = c.Style
			}
			b = append(b, c.Content...)
		}
		if cur != (Style{}) {
			b = append(b, "\x1b[m"...)
		}
	}
	return string(b)
}
