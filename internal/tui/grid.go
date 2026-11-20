package tui

// Cell is one cell of a grid. A wide grapheme takes two cells: the left one
// holds it and has Width 2, and the right one is empty and has Width 0.
type Cell struct {
	Style   Style
	Width   uint8
	Content string
}

// Blank is an empty cell in the default style.
var Blank = Cell{Content: " ", Width: 1}

// rowSpan is the half-open range [lo, hi) of dirty columns on one row.
// lo >= hi means the row is clean.
type rowSpan struct {
	lo, hi int
}

// Grid is a screen of cells. It remembers which cells of each row changed
// since they were last flushed to a terminal.
type Grid struct {
	w, h  int
	cells []Cell
	dirty []rowSpan
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
		g.dirty = make([]rowSpan, h)
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
	full := rowSpan{0, g.w}
	for y := range g.dirty {
		g.dirty[y] = full
	}
}

// At returns the cell at x, y.
func (g *Grid) At(x, y int) Cell { return g.cells[y*g.w+x] }

func (g *Grid) row(y int) []Cell { return g.cells[y*g.w : (y+1)*g.w] }

func (g *Grid) mark(y, lo, hi int) {
	d := &g.dirty[y]
	if d.lo >= d.hi {
		d.lo, d.hi = lo, hi
		return
	}
	if lo < d.lo {
		d.lo = lo
	}
	if hi > d.hi {
		d.hi = hi
	}
}

// set puts c at x, y, blanking what remains of wide cells it overwrites
// in part. c must fit in the row.
func (g *Grid) set(x, y int, c Cell) {
	row := g.row(y)
	if c.Width == 1 && row[x] == c {
		return
	}
	lo, hi := x, x+int(c.Width)
	if row[x].Width == 0 && x > 0 {
		row[x-1] = Blank
		lo = x - 1
	}
	if end := x + int(c.Width); row[end-1].Width == 2 && end < len(row) {
		row[end] = Blank
		hi = end + 1
	}
	row[x] = c
	if c.Width == 2 {
		row[x+1] = Cell{Style: c.Style}
	}
	g.mark(y, lo, hi)
}

// fill1 writes n copies of the 1-wide cell c starting at x, y. The range
// [x, x+n) must fit in the row.
func (g *Grid) fill1(x, y, n int, c Cell) {
	if n <= 0 {
		return
	}
	row := g.row(y)
	end := x + n
	lo, hi := end, x
	if row[x].Width == 0 && x > 0 {
		row[x-1] = Blank
		lo, hi = x-1, x
	}
	rightBlank := end < len(row) && row[end-1].Width == 2
	for i := x; i < end; i++ {
		if row[i] != c {
			row[i] = c
			if i < lo {
				lo = i
			}
			hi = i + 1
		}
	}
	if rightBlank {
		row[end] = Blank
		hi = end + 1
	}
	if lo < hi {
		g.mark(y, lo, hi)
	}
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
