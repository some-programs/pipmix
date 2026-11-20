package pipmix

import (
	"fmt"
	"testing"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
	"github.com/some-programs/pipmix/internal/tui"
)

// benchMixer returns a mixer on the playback tab of a w×h terminal with
// more streams than fit, all metered.
func benchMixer(w, h int) mixer {
	snap := testSnapshot()
	for i := range 20 {
		snap.Nodes = append(snap.Nodes, pw.Node{ID: uint32(100 + i), Class: pw.Playback, Name: fmt.Sprint("n", i), Title: fmt.Sprint("Player ", i), Channels: stereo(0.5)})
	}
	m := newMixer(pw.NewClient(nil), nil, pulse.NewMeter(nil, 0), quitKeys(), defaultOptions())
	m.snap = snap
	m.fill = make(map[uint32][]float64)
	for _, n := range snap.Nodes {
		m.fill[n.ID] = make([]float64, len(n.Channels))
	}
	m.setSize(w, h)
	m.refocus()
	m.setTab(tabPlayback)
	return m
}

// setBenchLevels moves every meter, keeping them below the faders.
func setBenchLevels(m *mixer, i int) {
	for id, levels := range m.fill {
		for ch := range levels {
			levels[ch] = float64((int(id)*13+ch*29+i*7)%100) / 250
		}
	}
}

type countWriter struct{ n int }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// BenchmarkFrameMeters measures a frame in which only the meters moved.
func BenchmarkFrameMeters(b *testing.B) {
	m := benchMixer(200, 50)
	g := tui.NewGrid(0, 0)
	var w countWriter
	s := tui.NewScreen(&w)
	m.draw(g)
	if err := s.Flush(g); err != nil {
		b.Fatal(err)
	}
	w.n = 0
	i := 0
	for b.Loop() {
		setBenchLevels(&m, i)
		i++
		m.drawMeters(g)
		if err := s.Flush(g); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(w.n)/float64(b.N), "bytes/frame")
}

// BenchmarkFrameFull measures a frame after an event, which redraws the
// whole screen.
func BenchmarkFrameFull(b *testing.B) {
	m := benchMixer(200, 50)
	g := tui.NewGrid(0, 0)
	var w countWriter
	s := tui.NewScreen(&w)
	i := 0
	for b.Loop() {
		setBenchLevels(&m, i)
		i++
		m.draw(g)
		if err := s.Flush(g); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(w.n)/float64(b.N), "bytes/frame")
}
