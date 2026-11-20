package tui

import (
	"bytes"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// vt is a terminal that knows only the sequences a Screen writes.
type vt struct {
	g      *Grid
	x, y   int
	style  Style
	inSync bool
}

func (t *vt) Write(p []byte) (int, error) {
	s := string(p)
	for len(s) > 0 {
		if strings.HasPrefix(s, "\x1b[") {
			end := strings.IndexAny(s[2:], "HJmhl") + 2
			params, final := s[2:end], s[end]
			s = s[end+1:]
			switch final {
			case 'H':
				t.x, t.y = 0, 0
				if params != "" {
					row, col, _ := strings.Cut(params, ";")
					t.y, _ = strconv.Atoi(row)
					t.x, _ = strconv.Atoi(col)
					t.y--
					t.x--
				}
			case 'J':
				t.g.Clear()
			case 'h', 'l':
				t.inSync = final == 'h'
			case 'm':
				t.style = Style{}
				fields := strings.Split(params, ";")
				for i := 0; i < len(fields); i++ {
					n, _ := strconv.Atoi(fields[i])
					switch {
					case n == 1:
						t.style.Attrs |= Bold
					case n == 2:
						t.style.Attrs |= Faint
					case n == 4:
						t.style.Attrs |= Underline
					case n == 7:
						t.style.Attrs |= Reverse
					case n >= 30 && n <= 37:
						t.style.Fg = Color(n - 30 + 1)
					case n >= 90 && n <= 97:
						t.style.Fg = Color(n - 90 + 9)
					case n == 58:
						c, _ := strconv.Atoi(fields[i+2])
						t.style.UnderlineColor = Color(c + 1)
						i += 2
					}
				}
			}
			continue
		}
		cl, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		s = s[len(cl):]
		t.g.set(t.x, t.y, Cell{Content: cl, Width: uint8(w), Style: t.style})
		t.x += w
	}
	return len(p), nil
}

func TestScreenFlush(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	words := []string{"a", "hello", "日本", "🔊", "█", "▓▓", "x", " ", "é", "…"}
	glyphs := []string{"a", "日", "🔊", "█", "▓", "x", " ", "é", "…"}
	styles := []Style{{}, {Fg: Red}, {Fg: BrightGreen, Attrs: Bold}, {Attrs: Underline | Reverse}, {Attrs: Underline, UnderlineColor: BrightBlack}}
	for _, size := range [][2]int{{1, 1}, {7, 3}, {30, 10}} {
		term := &vt{g: NewGrid(size[0], size[1])}
		s := NewScreen(term)
		g := NewGrid(size[0], size[1])
		for frame := range 300 {
			// Mostly small changes, as when meters move; sometimes a new
			// screen.
			if frame%50 == 0 {
				g.Clear()
			}
			for range r.IntN(8) {
				x, y := r.IntN(size[0]), r.IntN(size[1])
				p := g.Pen(x, y, x+r.IntN(size[0]-x+1))
				st := styles[r.IntN(len(styles))]
				switch r.IntN(3) {
				case 0:
					p.Repeat(glyphs[r.IntN(len(glyphs))], r.IntN(6), st)
				case 1:
					p.Text(words[r.IntN(len(words))], st)
					p.Pad(st)
				default:
					p.Text(words[r.IntN(len(words))], st)
				}
			}
			if frame == 150 {
				s.Redraw()
			}
			if err := s.Flush(g); err != nil {
				t.Fatal(err)
			}
			if got, want := term.g.String(), g.String(); got != want {
				t.Fatalf("%dx%d frame %d: terminal shows\n%s\nwant\n%s", size[0], size[1], frame, got, want)
			}
			if term.inSync {
				t.Fatalf("frame %d left synchronized output on", frame)
			}
		}
	}
}

func TestScreenWritesOnlyChanges(t *testing.T) {
	var out bytes.Buffer
	s := NewScreen(&out)
	g := NewGrid(40, 3)
	p := g.Pen(0, 1, 40)
	p.Text("volume ▓▓▓▓▓▓░░░░", Style{})
	if err := s.Flush(g); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := s.Flush(g); err != nil || out.Len() != 0 {
		t.Fatalf("flush without changes wrote %q, %v", out.String(), err)
	}
	p = g.Pen(13, 1, 40)
	p.Text("▓", Style{Fg: Green})
	if err := s.Flush(g); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[?2026h\x1b[2;14H\x1b[0;32m▓\x1b[?2026l"; out.String() != want {
		t.Errorf("one changed cell: wrote %q, want %q", out.String(), want)
	}
}
