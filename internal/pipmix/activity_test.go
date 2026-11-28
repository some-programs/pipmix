package pipmix

import (
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

// activityMixer returns a mixer on the Default tab with a default output,
// a default input, a FluidSynth stream, and a recording stream.
func activityMixer(opts options, hide ...hideRule) *mixer {
	m := newMixer(pw.NewClient(nil), nil, pulse.NewMeter(nil, 40), quitKeys(), opts)
	for i := range hide {
		if err := hide[i].check(); err != nil {
			panic(err)
		}
	}
	m.hide = hide
	m.snap = &pw.Snapshot{Connected: true, Nodes: []pw.Node{
		{ID: 10, Class: pw.Playback, Name: "FluidSynth", Props: map[string]string{"node.name": "FluidSynth", "object.serial": "100"}, Channels: stereo(1), State: pw.StateRunning, Active: true},
		{ID: 11, Class: pw.Playback, Name: "firefox", Props: map[string]string{"node.name": "firefox", "object.serial": "101"}, Channels: stereo(1)},
		{ID: 12, Class: pw.Playback, Name: "jack", JACK: true, Channels: stereo(1)},
		{ID: 20, Class: pw.Sink, Name: "sink", Props: map[string]string{"node.name": "sink"}, Channels: stereo(1), Default: true},
		{ID: 21, Class: pw.Source, Name: "mic", Props: map[string]string{"node.name": "mic"}, Channels: stereo(1), Default: true},
		{ID: 22, Class: pw.Source, Name: "other-mic", Channels: stereo(1)},
		{ID: 23, Class: pw.Sink, Name: "hdmi", Channels: stereo(1)},
		{ID: 30, Class: pw.Recording, Name: "obs", Props: map[string]string{"node.name": "obs"}, Channels: stereo(1)},
		{ID: 31, Class: pw.Recording, Name: "jack", JACK: true, Channels: stereo(1)},
	}}
	m.setSize(80, 40)
	m.refocus()
	return &m
}

// setNode applies f to node id in a new snapshot, as PipeWire would, and
// updates m at now.
func setNode(m *mixer, id uint32, f func(n *pw.Node), now time.Time) {
	snap := *m.snap
	snap.Nodes = slices.Clone(snap.Nodes)
	for i := range snap.Nodes {
		if snap.Nodes[i].ID == id {
			f(&snap.Nodes[i])
		}
	}
	m.snap = &snap
	m.refreshShown(now)
}

// setState sets node id's state, as setNode does.
func setState(m *mixer, id uint32, s pw.State, now time.Time) {
	setNode(m, id, func(n *pw.Node) {
		n.State = s
		n.Active = s == pw.StateRunning
	}, now)
}

// wantShown fails the test with the formatted message if the Default tab
// doesn't list node id.
func wantShown(t *testing.T, m *mixer, id uint32, format string, args ...any) {
	t.Helper()
	if got := defaultIDs(m); !slices.Contains(got, id) {
		t.Errorf(format+": rows %v", append(args, got)...)
	}
}

// wantHidden fails the test with the formatted message if the Default tab
// lists node id.
func wantHidden(t *testing.T, m *mixer, id uint32, format string, args ...any) {
	t.Helper()
	if got := defaultIDs(m); slices.Contains(got, id) {
		t.Errorf(format+": rows %v", append(args, got)...)
	}
}

func defaultIDs(m *mixer) []uint32 {
	var ids []uint32
	for _, it := range m.items(tabDefault) {
		ids = append(ids, it.id)
	}
	return ids
}

func TestRecordingRows(t *testing.T) {
	now := time.Now()
	m := activityMixer(defaultOptions())
	m.refreshShown(now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("nothing recording: %v, want %v", got, want)
	}

	setState(m, 30, pw.StateRunning, now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 30}; !slices.Equal(got, want) {
		t.Fatalf("recording: %v, want %v (recording last)", got, want)
	}
	if !m.redraw {
		t.Error("recording row appeared without redraw")
	}

	setState(m, 30, pw.StateIdle, now.Add(time.Second))
	wantShown(t, m, 30, "recording gone right after it stopped")
	if want := now.Add(time.Second + activeLinger); !m.act.wakeAt.Equal(want) {
		t.Errorf("wake at %v, want %v", m.act.wakeAt, want)
	}
	m.refreshShown(now.Add(time.Second + activeLinger))
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("after the linger: %v, want %v", got, want)
	}
	if !m.act.wakeAt.IsZero() {
		t.Errorf("wake at %v with nothing to wait for", m.act.wakeAt)
	}

	o := defaultOptions()
	o.recording = showAll
	m = activityMixer(o)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 30}; !slices.Equal(got, want) {
		t.Errorf("-default.recording=all: %v, want %v", got, want)
	}
	o.recording = showNone
	m = activityMixer(o)
	setState(m, 30, pw.StateRunning, now)
	wantHidden(t, m, 30, "-default.recording=off shows a recording")
}

func rule(match nodeMatch, when hideWhen, after time.Duration) hideRule {
	d := tomlDuration(after)
	r := hideRule{Match: match, When: when}
	if when != hideAlways {
		r.After = &d
	}
	return r
}

func nameIs(s string) nodeMatch { return nodeMatch{Name: regexp.MustCompile("^" + s + "$")} }

func TestHideAlwaysAndIdle(t *testing.T) {
	now := time.Now()
	m := activityMixer(defaultOptions(),
		rule(nameIs("sink"), hideAlways, 0),
		rule(nameIs("firefox"), hideIdle, 10*time.Second))
	m.refreshShown(now)
	if got, want := defaultIDs(m), []uint32{10, 12}; !slices.Equal(got, want) {
		t.Fatalf("hidden output and idle firefox: %v, want %v", got, want)
	}

	setState(m, 11, pw.StateRunning, now)
	if got, want := defaultIDs(m), []uint32{10, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("firefox running: %v, want %v", got, want)
	}
	setState(m, 11, pw.StateSuspended, now.Add(time.Second))
	wantShown(t, m, 11, "firefox hidden before after")
	m.refreshShown(now.Add(11 * time.Second))
	wantHidden(t, m, 11, "firefox shown after 10s idle")
	if got := m.items(tabPlayback); len(got) != 3 {
		t.Errorf("the Playback tab hides rows too: %d items", len(got))
	}
}

func TestHideFocused(t *testing.T) {
	now := time.Now()
	m := activityMixer(defaultOptions(), rule(nameIs("firefox"), hideIdle, time.Second))
	setState(m, 11, pw.StateRunning, now)
	items := m.items(tabDefault)
	i := slices.IndexFunc(items, func(it item) bool { return it.id == 11 })
	m.setFocus(items, i, 0)
	setState(m, 11, pw.StateIdle, now)
	m.refreshShown(now.Add(time.Second))
	wantHidden(t, m, 11, "focused row shown after its rule's after")
	if it, j := m.focused(); it.id == 11 || j != i {
		t.Errorf("focus on %d at %d, want the row that took %d's place, at %d", it.id, j, 11, i)
	}
}

func TestDeviceRows(t *testing.T) {
	now := time.Now()
	o := defaultOptions()
	o.outputs, o.inputs = showActive, showActive
	m := activityMixer(o)
	m.refreshShown(now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12}; !slices.Equal(got, want) {
		t.Fatalf("nothing in use: %v, want %v", got, want)
	}
	setState(m, 23, pw.StateRunning, now)
	setState(m, 22, pw.StateRunning, now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 23, 22}; !slices.Equal(got, want) {
		t.Fatalf("hdmi and other mic in use: %v, want %v (outputs, then inputs, at the bottom)", got, want)
	}
	setState(m, 30, pw.StateRunning, now)
	setState(m, 21, pw.StateRunning, now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 23, 21, 22, 30}; !slices.Equal(got, want) {
		t.Fatalf("recording from the default input too: %v, want %v (default input first)", got, want)
	}

	o.outputs, o.inputs = showAll, showAll
	m = activityMixer(o)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 23, 21, 22}; !slices.Equal(got, want) {
		t.Errorf("all devices: %v, want %v", got, want)
	}
	m = activityMixer(o, rule(nodeMatch{Type: new(nodeType(pw.Source))}, hideAlways, 0))
	m.refreshShown(now)
	setState(m, 30, pw.StateRunning, now)
	if got, want := defaultIDs(m), []uint32{20, 10, 11, 12, 23, 30}; !slices.Equal(got, want) {
		t.Errorf("all devices, inputs hidden: %v, want %v", got, want)
	}
}

func TestHideMuted(t *testing.T) {
	now := time.Now()
	m := activityMixer(defaultOptions(), rule(nodeMatch{Type: new(nodeType(pw.Playback))}, hideMuted, 10*time.Second))
	m.snap.Nodes[1].Mute = true // firefox starts muted
	m.snap.Nodes[2].Mute = true // a JACK client has no mute to hide it by
	m.refreshShown(now)
	if got, want := defaultIDs(m), []uint32{20, 10, 12}; !slices.Equal(got, want) {
		t.Fatalf("muted from the start: %v, want %v", got, want)
	}

	set := func(at time.Time, f func(n *pw.Node)) { setNode(m, 10, f, at) } // FluidSynth
	set(now, func(n *pw.Node) { n.Channels = stereo(0) })
	m.refreshShown(now.Add(time.Hour))
	wantShown(t, m, 10, "hidden at 0%%, unmuted")
	set(now, func(n *pw.Node) { n.Mute = true })
	m.refreshShown(now.Add(9 * time.Second))
	wantShown(t, m, 10, "hidden 9s after muted")
	m.refreshShown(now.Add(10 * time.Second))
	wantHidden(t, m, 10, "shown 10s after muted")
	set(now.Add(11*time.Second), func(n *pw.Node) { n.Mute = false })
	if got, want := defaultIDs(m), []uint32{20, 10, 12}; !slices.Equal(got, want) {
		t.Errorf("unmuted, a muted JACK client: %v, want %v", got, want)
	}

	// PipeWire lists a node before its volume and mute, unmuted until then.
	m = activityMixer(defaultOptions(), rule(nodeMatch{Type: new(nodeType(pw.Playback))}, hideMuted, 10*time.Second))
	m.snap.Nodes[1].Channels = nil
	m.refreshShown(now)
	setNode(m, 11, func(n *pw.Node) { n.Channels, n.Mute = stereo(1), true }, now.Add(time.Second))
	wantHidden(t, m, 11, "firefox, muted when its mute got known, shown")
}
