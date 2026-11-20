package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// oldFit is how pipmix fitted strings before it drew into grids.
func oldFit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func TestFitMatchesTruncate(t *testing.T) {
	texts := []string{
		"", "a", "abc", "hello world", "日本語のテキスト", "a日b本c", "🔊 Speakers", "été",
		"x🔇y", "Firefox: Some Track — Artist", "ab日",
	}
	for _, s := range texts {
		for w := range 14 {
			g := NewGrid(16, 1)
			p := g.Pen(1, 0, 16)
			p.Fit(s, w, Style{}, Style{})
			got := strings.TrimSuffix(ansi.Strip(g.String())[1:], strings.Repeat(" ", 15-w))
			if want := oldFit(s, w); got != want {
				t.Errorf("Fit(%q, %d) = %q, want %q", s, w, got, want)
			}
			if p.X() != 1+w {
				t.Errorf("Fit(%q, %d) ends at %d, want %d", s, w, p.X(), 1+w)
			}
		}
	}
}

func TestClippedSub(t *testing.T) {
	// The parent clips a field that is wider than what is left of it: the
	// padding is cut, so the text ends in an ellipsis as fit(a+fit(b)) did.
	for w := range 12 {
		g := NewGrid(20, 1)
		p := g.Pen(0, 0, w)
		p.Fit("🔊", 6, Style{}, Style{})
		p.Text("title", Style{})
		got := ansi.Truncate(ansi.Strip(g.String()), w, "")
		if want := oldFit(oldFit("🔊", 6)+"title", w); got != want {
			t.Errorf("width %d: %q, want %q", w, got, want)
		}
	}
}

func TestWideOverwrite(t *testing.T) {
	g := NewGrid(6, 1)
	p := g.Pen(0, 0, 6)
	p.Text("日本語", Style{})
	q := g.Pen(1, 0, 6)
	q.Text("x", Style{})
	r := g.Pen(4, 0, 6)
	r.Text("y", Style{})
	if got, want := g.String(), " x本y "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
