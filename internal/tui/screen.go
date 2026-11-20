package tui

import (
	"io"
	"strconv"
)

// maxGap is the most unchanged cells that a flush rewrites to save moving
// the cursor past them.
const maxGap = 4

// Screen shows grids on a terminal. It remembers what the terminal shows
// and writes only the cells that differ.
type Screen struct {
	w      io.Writer
	front  *Grid // what the terminal shows
	buf    []byte
	style  Style // the terminal's current style
	cx, cy int   // the cursor; cx is -1 when not known
	reset  bool  // the next flush clears the terminal first
}

// NewScreen returns a screen that writes to w.
func NewScreen(w io.Writer) *Screen {
	return &Screen{w: w, front: NewGrid(0, 0), cx: -1, reset: true}
}

// Redraw makes the next flush clear the terminal and draw every cell.
func (s *Screen) Redraw() { s.reset = true }

// Flush updates the terminal to show g. It compares only the rows of g
// that changed since the last flush.
func (s *Screen) Flush(g *Grid) error {
	// Synchronized output keeps the terminal from showing half a frame.
	const syncStart, syncEnd = "\x1b[?2026h", "\x1b[?2026l"
	s.buf = append(s.buf[:0], syncStart...)
	if w, h := s.front.Size(); s.reset || w != g.w || h != g.h {
		s.front.Resize(g.w, g.h)
		s.buf = appendSGR(s.buf, Style{})
		s.buf = append(s.buf, "\x1b[H\x1b[2J"...)
		s.style, s.cx, s.reset = Style{}, -1, false
		for y := range g.dirty {
			g.dirty[y] = true
		}
	}
	for y, dirty := range g.dirty {
		if dirty {
			s.flushRow(g, y)
			g.dirty[y] = false
		}
	}
	if len(s.buf) == len(syncStart) {
		return nil
	}
	s.buf = append(s.buf, syncEnd...)
	_, err := s.w.Write(s.buf)
	return err
}

func (s *Screen) flushRow(g *Grid, y int) {
	row, front := g.row(y), s.front.row(y)
	for x := 0; x < len(row); {
		if row[x] == front[x] {
			x++
			continue
		}
		if row[x].Width == 0 && x > 0 {
			x-- // Start at the left half of a wide cell.
		}
		for x < len(row) {
			if row[x] == front[x] {
				j := x
				for j < len(row) && row[j] == front[j] {
					j++
				}
				if j == len(row) || j-x > maxGap {
					x = j
					break
				}
			}
			x += s.writeCell(row, x, y)
		}
	}
	copy(front, row)
}

// writeCell writes the cell at x, y and returns how many cells it covers.
func (s *Screen) writeCell(row []Cell, x, y int) int {
	c := row[x]
	if c.Width == 0 {
		// The right half of a wide cell written with its left half, or
		// one left over from a wide cell that no longer fits.
		if x == 0 || row[x-1].Width != 2 {
			c = Cell{Content: " ", Width: 1, Style: c.Style}
		} else {
			return 1
		}
	}
	if s.cx != x || s.cy != y {
		s.buf = append(s.buf, "\x1b["...)
		s.buf = strconv.AppendInt(s.buf, int64(y+1), 10)
		s.buf = append(s.buf, ';')
		s.buf = strconv.AppendInt(s.buf, int64(x+1), 10)
		s.buf = append(s.buf, 'H')
	}
	if c.Style != s.style {
		s.buf = appendSGR(s.buf, c.Style)
		s.style = c.Style
	}
	s.buf = append(s.buf, c.Content...)
	s.cx, s.cy = x+int(c.Width), y
	if c.Width == 2 {
		// Terminals disagree on the width of some graphemes, so move to
		// the next cell rather than trusting where this one ends.
		s.cx = -1
	}
	return int(c.Width)
}
