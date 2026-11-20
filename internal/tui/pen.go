package tui

import "github.com/charmbracelet/x/ansi"

// Ellipsis ends text that a pen clips.
const Ellipsis = "…"

// Pen writes text along part of a grid row. Text that does not fit ends
// in an [Ellipsis], and the pen writes nothing more.
type Pen struct {
	g     *Grid
	y     int
	x     int  // the next cell
	start int  // the first cell
	end   int  // cells from here on are clipped
	want  int  // where [Pen.Pad] pads to; past end if a parent pen clips
	attrs Attr // added to every style
	full  bool // clipped; nothing more is written
}

// X returns the column of the next cell p writes.
func (p *Pen) X() int { return p.x }

// AddAttrs adds attrs to the style of everything p writes from now on.
func (p *Pen) AddAttrs(attrs Attr) { p.attrs |= attrs }

// Text writes s in style st. Control characters and other graphemes of no
// width are dropped.
func (p *Pen) Text(s string, st Style) {
	st.Attrs |= p.attrs
	for len(s) > 0 && !p.full {
		var cl string
		var w int
		if c := s[0]; c >= 0x20 && c < 0x7f && (len(s) == 1 || s[1] < 0x80) {
			cl, w = s[:1], 1
		} else {
			cl, w = ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
			w = min(w, 2)
		}
		s = s[len(cl):]
		if w == 0 {
			continue
		}
		if p.x+w > p.end {
			p.clip(st)
			return
		}
		p.g.set(p.x, p.y, Cell{Content: cl, Width: uint8(w), Style: st})
		p.x += w
	}
}

// Repeat writes n copies of the grapheme glyph in style st.
func (p *Pen) Repeat(glyph string, n int, st Style) {
	if n <= 0 || p.full {
		return
	}
	st.Attrs |= p.attrs
	w := min(ansi.StringWidth(glyph), 2)
	if w == 0 {
		return
	}
	c := Cell{Content: glyph, Width: uint8(w), Style: st}
	for range n {
		if p.x+w > p.end {
			p.clip(st)
			return
		}
		p.g.set(p.x, p.y, c)
		p.x += w
	}
}

// Pad writes spaces in style st up to the width of p. If a parent pen clips
// p, Pad clips there as text would.
func (p *Pen) Pad(st Style) {
	st.Attrs |= p.attrs
	c := Cell{Content: " ", Width: 1, Style: st}
	for ; p.x < min(p.want, p.end); p.x++ {
		p.g.set(p.x, p.y, c)
	}
	if p.want > p.end && !p.full {
		p.clip(st)
	}
}

// Sub returns a pen for the next w cells of p, clipped where p clips. Hand
// it back to p with [Pen.Join] when done.
func (p *Pen) Sub(w int) Pen {
	c := *p
	c.start, c.want = p.x, p.x+max(w, 0)
	c.end = min(c.want, p.end)
	return c
}

// Join continues p after sub, a pen from [Pen.Sub].
func (p *Pen) Join(sub Pen) {
	p.x = sub.x
	if sub.full && sub.want > sub.end {
		p.full = true
	}
}

// Fit writes s in style st into the next w cells, clipping it or padding it
// with spaces in style pad.
func (p *Pen) Fit(s string, w int, st, pad Style) {
	c := p.Sub(w)
	c.Text(s, st)
	c.Pad(pad)
	p.Join(c)
}

// clip ends the text with an ellipsis in style st, taking back the last
// grapheme if the ellipsis does not fit after it.
func (p *Pen) clip(st Style) {
	p.full = true
	x := p.x
	if x >= p.end {
		x = p.end - 1
		if x > p.start && p.g.At(x, p.y).Width == 0 {
			x--
		}
	}
	if x < p.start {
		return
	}
	p.g.set(x, p.y, Cell{Content: Ellipsis, Width: 1, Style: st})
	p.x = x + 1
}
