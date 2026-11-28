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

func TestShortDeviceName(t *testing.T) {
	for name, want := range map[string]string{
		"Arctis Nova Pro Wireless Analog Stereo":                       "Arctis Nova Pro Wireless",
		"Arctis Nova Pro Wireless Mono":                                "Arctis Nova Pro Wireless",
		"DUO-CAPTURE EX Analog Stereo":                                 "DUO-CAPTURE EX",
		"AD102 High Definition Audio Controller Digital Stereo (HDMI)": "AD102 High Definition Audio Controller (HDMI)",
		"Built-in Audio Analog Surround 5.1":                           "Built-in Audio",
		"Stereo":                                                       "Stereo",
		"My Speakers":                                                  "My Speakers",
	} {
		if got := shortDeviceName(name); got != want {
			t.Errorf("shortDeviceName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestStreamLabels(t *testing.T) {
	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	chrome := map[string]string{"application.process.id": "14744"}
	m.snap = &pw.Snapshot{Connected: true, Nodes: []pw.Node{
		{ID: 35, Class: pw.Sink, Name: "duo", Title: "DUO-CAPTURE EX Analog Stereo", Channels: stereo(1), Default: true},
		{ID: 44, Class: pw.Source, Name: "arctis", Title: "Arctis Nova Pro Wireless Mono", Channels: stereo(1)},
		{ID: 45, Class: pw.Sink, Name: "arctis-out", Title: "Arctis Nova Pro Wireless Analog Stereo", Channels: stereo(1)},
		{ID: 98, Class: pw.Recording, Title: "Google Chrome input: RecordStream", Client: "Google Chrome input", Props: chrome, From: []uint32{44}, Channels: stereo(1)},
		{ID: 144, Class: pw.Recording, Title: "Google Chrome input: RecordStream", Client: "Google Chrome input", Props: chrome, From: []uint32{44}, Channels: stereo(1)},
		{ID: 150, Class: pw.Recording, Title: "Google Chrome input: RecordStream", Client: "Google Chrome input", Props: chrome, From: []uint32{35}, Channels: stereo(1)},
		{ID: 133, Class: pw.Playback, Title: "mpd", Channels: stereo(1), To: []uint32{35}},
		{ID: 220, Class: pw.Playback, Title: "Google Chrome: Playback", Props: chrome, To: []uint32{35}, Channels: stereo(1)},
	}}
	m.setSize(100, 30)
	m.setTab(tabRecording)
	m.refocus()

	view := ansi.Strip(m.view())
	for _, want := range []string{"Arctis Nova Pro Wireless Mono", "monitor of DUO-CAPTURE EX Analog Stereo"} {
		if !strings.Contains(view, want) {
			t.Errorf("Recording tab lacks %q:\n%s", want, view)
		}
	}

	m.setTab(tabPlayback)
	m.refocus()
	if view := ansi.Strip(m.view()); strings.Contains(view, "DUO-CAPTURE") {
		t.Errorf("playback streams labelled with one output in use:\n%s", view)
	}
	m.snap.Nodes[7].To = []uint32{45}
	if view := ansi.Strip(m.view()); !strings.Contains(view, "Arctis Nova Pro Wireless Analog Stereo") || !strings.Contains(view, "DUO-CAPTURE EX Analog Stereo") {
		t.Errorf("playback streams not labelled with two outputs in use:\n%s", view)
	}

	m.setSize(80, 30)
	m.setTab(tabRecording)
	m.refocus()
	if view := ansi.Strip(m.view()); !strings.Contains(view, "monitor of DUO-CAPTURE EX") || strings.Contains(view, "EX Analog") {
		t.Errorf("narrow Recording tab doesn't shorten device names:\n%s", view)
	}
}
