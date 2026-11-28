package pipmix

import (
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
	"github.com/some-programs/pipmix/internal/tui"
)

func meterSnapshot() *pw.Snapshot {
	s := testSnapshot()
	for i := range 3 {
		s.Nodes[i].Props = map[string]string{"object.serial": strconv.Itoa(100 + i)}
		s.Nodes[i].Active = true
	}
	s.Nodes = append(s.Nodes,
		pw.Node{ID: 40, Class: pw.Recording, Name: "rec", Title: "Recorder", Channels: stereo(1)},
		pw.Node{ID: 41, Class: pw.Playback, Name: "jack", Title: "jackd", JACK: true, Active: true, Channels: []pw.Channel{{Name: "out_1", Volume: 1}, {Name: "out_2", Volume: 1}}},
		pw.Node{ID: 41, Class: pw.Recording, Name: "jack", Title: "jackd", JACK: true},
		pw.Node{ID: 42, Class: pw.Source, Name: "mic", Title: "Mic", Channels: []pw.Channel{{Name: "MONO", Volume: 1}}},
	)
	return s
}

func targetKeys(targets map[uint32]pulse.Target) []uint32 {
	var keys []uint32
	for k := range targets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestMeterTargets(t *testing.T) {
	m := testMixer()
	m.snap = meterSnapshot()
	m.refocus()

	got := m.meterTargets()
	if keys := targetKeys(got); !slices.Equal(keys, []uint32{10, 11, 12, 41}) {
		t.Fatalf("playback targets %v, want 10 11 12 41", keys)
	}
	want := pulse.Target{Kind: pulse.Stream, Serial: 101, Channels: []string{"FL", "FR"}}
	if !got[11].Equal(want) {
		t.Errorf("target of 11 = %+v, want %+v", got[11], want)
	}
	m.snap.Nodes[1].Active = false
	if keys := targetKeys(m.meterTargets()); !slices.Equal(keys, []uint32{10, 12, 41}) {
		t.Errorf("targets with 11 idle %v, want 10 12 41", keys)
	}
	m.snap.Nodes[1].Active = true
	wantJACK := pulse.Target{Kind: pulse.JACK, Serial: 41, Channels: []string{"out_1", "out_2"}}
	if !got[41].Equal(wantJACK) {
		t.Errorf("target of 41 = %+v, want %+v", got[41], wantJACK)
	}

	m.opts.jack = false
	if keys := targetKeys(m.meterTargets()); !slices.Equal(keys, []uint32{10, 11, 12}) {
		t.Fatalf("playback targets without jack %v, want 10 11 12", keys)
	}
	m.opts.jack = true

	m.setTab(tabOutputs)
	want = pulse.Target{Kind: pulse.Sink, Name: "sink", Channels: []string{"FL", "FR"}}
	if got := m.meterTargets(); !got[20].Equal(want) || len(got) != 1 {
		t.Errorf("output targets %+v, want only %+v", got, want)
	}
	m.setTab(tabInputs)
	want = pulse.Target{Kind: pulse.Source, Name: "mic", Channels: []string{"MONO"}}
	if got := m.meterTargets(); !got[42].Equal(want) || len(got) != 1 {
		t.Errorf("input targets %+v, want only %+v", got, want)
	}
	m.setTab(tabRecording)
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("recording targets %+v, want none", got)
	}

	m.setTab(tabPlayback)
	m.setSize(60, 3)
	if keys := targetKeys(m.meterTargets()); !slices.Equal(keys, []uint32{10}) {
		t.Errorf("targets on a short screen %v, want 10", keys)
	}
	m.handleKey("j")
	if keys := targetKeys(m.meterTargets()); !slices.Equal(keys, []uint32{11}) {
		t.Errorf("targets after j %v, want 11", keys)
	}

	m.setSize(60, 20)
	m.handleKey("?")
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("targets under the help %v, want none", targetKeys(got))
	}
	m.handleKey("?")
	m.meter = pulse.NewMeter(nil, 0)
	m.handleKey("a")
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("targets with meters off %v, want none", targetKeys(got))
	}
	if m.look() != meterOff {
		t.Errorf("look with meters off %d, want off", m.look())
	}
	m.handleKey("a")
	if got := m.meterTargets(); len(got) == 0 || m.look() != meterSolid {
		t.Errorf("a again: targets %v, look %d; want some, solid", targetKeys(got), m.look())
	}
}

func TestLevels(t *testing.T) {
	m := testMixer()
	m.snap = meterSnapshot()
	now := time.Now()
	both := func(l map[uint32][]float64) *pulse.Snapshot { return &pulse.Snapshot{Peaks: l, Averages: l} }
	near := func(got, want []float64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				return false
			}
		}
		return true
	}

	// Levels on the volume's scale: a node's first ones are shown as
	// they are.
	m.setLevels(both(map[uint32][]float64{
		10: {0.125, 1},     // a stream: after its volume already
		12: {1, 1},         // muted
		20: {0.125, 0.125}, // a sink at 100%: before its volume
		41: {0.125, 0.512}, // a duplex JACK client: uses its playback channels
		99: {1},            // gone
		11: {1},            // channels don't match
	}), now)
	for id, want := range map[uint32][]float64{10: {0.5, 1}, 12: {0, 0}, 20: {0.5, 0.5}, 41: {0.5, 0.8}, 99: nil, 11: nil} {
		if !near(m.fill[id], want) || !near(m.dot[id], want) {
			t.Errorf("node %d: fill %v, peak %v; want %v", id, m.fill[id], m.dot[id], want)
		}
	}
	// A sink's are scaled by its volume.
	m.snap.Nodes[3].Channels = stereo(0.5)
	m.fill, m.dot = nil, nil
	m.setLevels(both(map[uint32][]float64{20: {1, 0.125}}), now)
	if want := []float64{0.5, 0.25}; !near(m.fill[20], want) {
		t.Errorf("sink at 50%%: %v, want %v", m.fill[20], want)
	}

	// The fill eases: up by attack, down by release, then settles.
	sm := smoothings[m.smoothing]
	m.fill, m.dot, m.dotHold = nil, nil, nil
	m.setLevels(both(map[uint32][]float64{10: {0.125, 0.125}}), now) // shown as 0.5
	dt := 20 * time.Millisecond
	if !m.setLevels(both(map[uint32][]float64{10: {1, 0}}), now.Add(dt)) {
		t.Error("setLevels doesn't report moving levels")
	}
	up := 0.5 + (1-0.5)*(1-math.Exp(-dt.Seconds()/sm.attack.Seconds()))
	down := 0.5 * math.Exp(-dt.Seconds()/sm.release.Seconds())
	if want := []float64{up, down}; !near(m.fill[10], want) {
		t.Errorf("fill after %v: %v, want %v", dt, m.fill[10], want)
	}
	// The peak cell rose at once, and holds its fall.
	if want := []float64{1, 0.5}; !near(m.dot[10], want) {
		t.Errorf("peak after %v: %v, want %v", dt, m.dot[10], want)
	}
	later := now.Add(m.opts.peakHold + dt + 100*time.Millisecond)
	m.setLevels(both(map[uint32][]float64{10: {1, 0}}), later)
	if want := []float64{1, 0.5 - m.opts.limit*(m.opts.peakHold+100*time.Millisecond).Seconds()}; !near(m.dot[10], []float64{1, max(want[1], 0)}) {
		t.Errorf("peak after the hold: %v, want %v", m.dot[10], want)
	}
	if m.setLevels(both(map[uint32][]float64{10: {1, 0}}), later.Add(time.Hour)) {
		t.Error("levels still move after an hour")
	}
	if want := []float64{1, 0}; !near(m.fill[10], want) || !near(m.dot[10], want) {
		t.Errorf("settled: fill %v, peak %v; want %v", m.fill[10], m.dot[10], want)
	}

	// The fill shows the average, or the peak; the peak cell shows the
	// peak with the average. The folded line shows the loudest channel.
	m.fill = nil
	m.setLevels(&pulse.Snapshot{
		Peaks:    map[uint32][]float64{10: {0.008, 0.343}},
		Averages: map[uint32][]float64{10: {0.001, 0.064}},
	}, later.Add(2*time.Hour))
	it := m.items(tabPlayback)[0]
	for _, tc := range []struct {
		mode        meterMode
		ch          int
		level, peak float64
	}{
		{modeAveragePeak, -1, 0.4, 0.7},
		{modeAveragePeak, 0, 0.1, 0.2},
		{modeAverage, 1, 0.4, 0},
	} {
		m.meterMode = tc.mode
		if l, p := m.level(it, tc.ch), m.peak(it, tc.ch); math.Abs(l-tc.level) > 1e-9 || math.Abs(p-tc.peak) > 1e-9 {
			t.Errorf("%s, channel %d: level %v, peak %v; want %v, %v", meterModeFlags[tc.mode], tc.ch, l, p, tc.level, tc.peak)
		}
	}
	m.meterMode = modePeak
	m.fill = nil
	m.setLevels(&pulse.Snapshot{Peaks: map[uint32][]float64{10: {0.008, 0.343}}}, later.Add(3*time.Hour))
	if l, p := m.level(it, -1), m.peak(it, -1); math.Abs(l-0.7) > 1e-9 || p != 0 {
		t.Errorf("peak mode: level %v, peak %v; want 0.7, 0", l, p)
	}
}

func TestMeterFocus(t *testing.T) {
	m := testMixer()
	m.snap = meterSnapshot()
	m.update(tui.BlurEvent{})
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("targets while not focused %v, want none", targetKeys(got))
	}
	m.update(tui.FocusEvent{})
	if got := m.meterTargets(); len(got) == 0 {
		t.Error("no targets once focused again")
	}
}

func TestBarLevel(t *testing.T) {
	for _, tc := range []struct {
		v, level, peak float64
		muted          bool
		look           meterLook
		want           string
	}{
		{1, 0.5, 0, false, meterSolid, "█████▓▓▓▓▓█░░░░░"},
		{1, 0.5, 0, false, meterShade, "█████▒▒▒▒▒█░░░░░"},
		{1, 0.5, 0, false, meterDot, "▓▓▓▓█▓▓▓▓▓█░░░░░"},
		{1, 0.5, 0, false, meterOff, "▓▓▓▓▓▓▓▓▓▓█░░░░░"},
		{0.5, 1, 0, false, meterSolid, "██████░░░░░░░░░░"},
		{1.5, 1.2, 0, false, meterSolid, "████████████▓▓▓█"},
		{1, 0.3, 0.7, false, meterSolid, "███▓▓▓█▓▓▓█░░░░░"},
		{1, 0.5, 0, true, meterSolid, "▓▓▓▓▓▓▓▓▓▓█░░░░░"},
	} {
		got := penText(16, func(p *tui.Pen) { drawBar(p, tc.v, tc.level, tc.peak, 16, 1.5, tc.muted, tc.look) })
		if got != tc.want {
			t.Errorf("bar(%v, %v, %v, 16, %v, look %d) = %q, want %q", tc.v, tc.level, tc.peak, tc.muted, tc.look, got, tc.want)
		}
	}
	for _, tc := range []struct {
		level, peak float64
		look        meterLook
		want        string
	}{
		{0, 0, meterSolid, "░░░░░░░░░░░░░░░░"},
		{0.3, 0.7, meterSolid, "███░░░█░░░░░░░░░"},
		{0.5, 0, meterDot, "░░░░█░░░░░░░░░░░"},
		{0.5, 0.7, meterOff, "░░░░░░░░░░░░░░░░"},
	} {
		got := penText(16, func(p *tui.Pen) { drawMeterBar(p, tc.level, tc.peak, 16, 1.5, tc.look) })
		if got != tc.want {
			t.Errorf("meterBar(%v, %v, 16, look %d) = %q, want %q", tc.level, tc.peak, tc.look, got, tc.want)
		}
	}
}

func TestMeterFadeIn(t *testing.T) {
	m := testMixer()
	m.snap = meterSnapshot()
	it := m.items(tabPlayback)[0]
	now := time.Now()
	snap := &pulse.Snapshot{Peaks: map[uint32][]float64{10: {1, 1}}, Averages: map[uint32][]float64{10: {1, 1}}}
	for _, tc := range []struct {
		after time.Duration
		want  float64
	}{
		{0, 0},
		{fadeIn / 2, 0.5},
		{fadeIn, 1},
	} {
		moving := m.setLevels(snap, now.Add(tc.after))
		l, p := m.level(it, -1), m.peak(it, -1)
		if math.Abs(l-tc.want) > 1e-9 || math.Abs(p-tc.want) > 1e-9 {
			t.Errorf("after %v: level %v, peak %v; want %v", tc.after, l, p, tc.want)
		}
		if wantMoving := tc.after < fadeIn; moving != wantMoving {
			t.Errorf("after %v: moving %v, want %v", tc.after, moving, wantMoving)
		}
	}

	m.setLevels(&pulse.Snapshot{}, now.Add(time.Second))
	m.setLevels(snap, now.Add(2*time.Second))
	if l := m.level(it, -1); l != 0 {
		t.Errorf("restarted meter's level %v, want 0", l)
	}
}

// TestMeterWaitOnce checks that a meter wait is never started while one is
// pending.
func TestMeterWaitOnce(t *testing.T) {
	m := testMixer()
	m.snap = meterSnapshot()
	m.meter = pulse.NewMeter(nil, 0)
	m.meterWaiting = true // as after init

	for _, msg := range []message{tui.BlurEvent{}, tui.FocusEvent{}} {
		if cmd := m.update(msg); cmd != nil {
			t.Errorf("%T with a wait pending: got a command, want none", msg)
		}
	}
	// A snapshot with nothing moving waits again, once.
	if cmd := m.update(meterMsg{}); cmd == nil || !m.meterWaiting {
		t.Fatal("snapshot with settled levels: no new wait")
	}
	if cmd := m.update(tui.FocusEvent{}); cmd != nil {
		t.Error("focus after the new wait: got a command, want none")
	}
}

func TestMeterBlurClearsLevels(t *testing.T) {
	for _, ticking := range []bool{false, true} {
		m := testMixer()
		m.snap = meterSnapshot()
		m.refocus()
		it := m.items(tabPlayback)[0]
		snap := &pulse.Snapshot{Averages: map[uint32][]float64{it.id: {0.5, 0.5}}}
		m.setLevels(snap, time.Now())
		m.setLevels(snap, time.Now().Add(time.Second)) // faded in
		if m.level(it, -1) == 0 {
			t.Fatal("no level to clear")
		}
		m.ticking, m.redraw = ticking, false
		m.update(tui.BlurEvent{})
		if ticking {
			m.tickMetering()
		}
		if l := m.level(it, -1); l != 0 || !m.redraw || m.ticking {
			t.Errorf("ticking %v, after blur: level %v, redraw %v, ticking %v; want 0, true, false", ticking, l, m.redraw, m.ticking)
		}
	}
}

func TestRecordingMeterTarget(t *testing.T) {
	s := &pw.Snapshot{Nodes: []pw.Node{
		{ID: 1, Class: pw.Source, Name: "mic", Channels: stereo(1)},
		{ID: 2, Class: pw.Sink, Name: "speakers", Channels: stereo(1)},
		{ID: 3, Class: pw.Recording, Name: "claude", From: []uint32{1}, Channels: []pw.Channel{{Name: "MONO", Volume: 0.5}}},
		{ID: 4, Class: pw.Recording, Name: "obs", From: []uint32{2}, Channels: stereo(1)},
		{ID: 5, Class: pw.Recording, Name: "both", From: []uint32{1, 2}, Channels: stereo(1)},
		{ID: 6, Class: pw.Recording, Name: "unlinked", Channels: stereo(1)},
	}}
	for _, tc := range []struct {
		id   uint32
		want pulse.Target
		ok   bool
	}{
		{3, pulse.Target{Kind: pulse.Source, Name: "mic", Channels: []string{"MONO"}}, true},
		{4, pulse.Target{Kind: pulse.Sink, Name: "speakers", Channels: []string{"FL", "FR"}}, true},
		{5, pulse.Target{}, false},
		{6, pulse.Target{}, false},
	} {
		got, ok := meterTarget(s, snapNode(s, tc.id))
		if ok != tc.ok || ok && !got.Equal(tc.want) {
			t.Errorf("node %d: %v, %v; want %v, %v", tc.id, got, ok, tc.want, tc.ok)
		}
	}
	// The source's level, through the stream's volume.
	if got := scale(&s.Nodes[2], []float64{1}, nil); len(got) != 1 || got[0] != 0.5 {
		t.Errorf("recording at 50%% scaled to %v, want [0.5]", got)
	}
}
