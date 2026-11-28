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

// With -meter.enable=false no meter is drawn, and a silent rule still
// listens to the node it matches, at the low rate.
func TestHideSilentMetersDisabled(t *testing.T) {
	now := time.Now()
	opts := defaultOptions()
	opts.meters = metersOff
	m := activityMixer(opts, rule(nameIs("FluidSynth"), hideSilent, 30*time.Second))
	m.refreshShown(now)
	m.watchMeters()
	if keys := targetKeys(m.watched); !slices.Equal(keys, []uint32{10}) {
		t.Fatalf("metered with -meter.enable=false: %v, want only FluidSynth", keys)
	}
	if got := m.watched[10].Rate; got != listenRate {
		t.Errorf("FluidSynth metered at rate %d, want %d", got, listenRate)
	}
}

func TestHideSilent(t *testing.T) {
	t.Run("heard, then silent", func(t *testing.T) {
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("FluidSynth"), hideSilent, 30*time.Second))
		m.refreshShown(now)
		wantShown(t, m, 10, "playing FluidSynth hidden before the meter has levels for it")
		if targets, _ := m.silentTargets(nil); targets[10].Serial != 100 {
			t.Errorf("FluidSynth not metered: %v", targets)
		}

		quiet := &pulse.Snapshot{Peaks: map[uint32][]float64{10: {0.00005, 0}}}
		m.hear(quiet, now)
		wantHidden(t, m, 10, "FluidSynth shown at -86 dBFS")
		// Hidden, its levels move no meter, so the render loop can rest.
		m.watchMeters()
		levels := map[uint32][]float64{10: {0.00005, 0}}
		m.setLevels(&pulse.Snapshot{Peaks: levels, Averages: levels}, now)
		levels = map[uint32][]float64{10: {0.0001, 0.0003}}
		if m.setLevels(&pulse.Snapshot{Peaks: levels, Averages: levels}, now.Add(20*time.Millisecond)) {
			t.Error("levels of hidden FluidSynth keep the meters moving")
		}
		loud := &pulse.Snapshot{Peaks: map[uint32][]float64{10: {0, 0.5}}}
		m.hear(loud, now.Add(time.Second))
		wantShown(t, m, 10, "playing FluidSynth hidden")
		if want := now.Add(31 * time.Second); !m.act.wakeAt.Equal(want) {
			t.Errorf("wake at %v, want %v", m.act.wakeAt, want)
		}

		// With the terminal not focused, the meters on screen pause, but
		// FluidSynth's goes on, at the lower rate that is enough for the
		// rule.
		m.watchMeters()
		if got := m.watched[10].Rate; got != 0 {
			t.Errorf("FluidSynth metered at rate %d while its meter shows, want 0 for the meter's rate", got)
		}
		m.blurred = true
		m.watchMeters()
		if keys := targetKeys(m.watched); !slices.Equal(keys, []uint32{10}) {
			t.Errorf("metered while the terminal isn't focused: %v, want only FluidSynth", keys)
		}
		if got := m.watched[10].Rate; got != listenRate {
			t.Errorf("FluidSynth metered at rate %d while the terminal isn't focused, want %d", got, listenRate)
		}
		m.opts.meterRate = 2
		m.watchMeters()
		if got := m.watched[10].Rate; got != 2 {
			t.Errorf("FluidSynth metered at rate %d with -meter.rate 2, want 2", got)
		}
	})
	t.Run("at a low volume", func(t *testing.T) {
		// At 19% the volume multiplies levels by 0.19 cubed, about 0.0069.
		// A peak of 0.00005 is below the threshold of 0.0001 as measured,
		// and above it as the level at 100%, about 0.0073.
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("FluidSynth"), hideSilent, 10*time.Second))
		m.refreshShown(now)
		peaks := &pulse.Snapshot{Peaks: map[uint32][]float64{10: {0.00005, 0}}}
		m.hear(peaks, now)
		wantHidden(t, m, 10, "FluidSynth at 100% shown at -86 dBFS")

		setNode(m, 10, func(n *pw.Node) { n.Channels = stereo(0.19) }, now)
		m.hear(&pulse.Snapshot{Peaks: peaks.Peaks}, now.Add(time.Second))
		wantShown(t, m, 10, "FluidSynth at 19% hidden while it sends -43 dBFS")
		// The same factor applies to the noise floor: 0.0000005 measured
		// at 19% is about -83 dBFS at 100%, below the threshold.
		m.hear(&pulse.Snapshot{Peaks: map[uint32][]float64{10: {0.0000005, 0}}}, now.Add(2*time.Second))
		m.refreshShown(now.Add(11*time.Second + time.Millisecond))
		wantHidden(t, m, 10, "FluidSynth at 19% shown 10s after its last peak above the threshold")

		// Output devices are measured before their volume.
		if got := volumeGain(&pw.Node{Class: pw.Sink, Channels: stereo(0.5)}); got != nil {
			t.Errorf("volumeGain of an output device = %v, want nil", got)
		}
	})
	t.Run("unmetered", func(t *testing.T) {
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("obs"), hideSilent, time.Second))
		setState(m, 30, pw.StateRunning, now)
		m.refreshShown(now.Add(time.Hour))
		wantShown(t, m, 30, "silent rule hid a stream it can't meter")
	})
	t.Run("at 0% or muted", func(t *testing.T) {
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("FluidSynth"), hideSilent, 10*time.Second))
		m.refreshShown(now)
		m.hear(&pulse.Snapshot{Peaks: map[uint32][]float64{10: {0, 0}}}, now)
		m.refreshShown(now)
		wantHidden(t, m, 10, "never-heard FluidSynth shown")
		for i, tc := range []struct {
			name string
			set  func(n *pw.Node)
		}{
			{"at 0%", func(n *pw.Node) { n.Channels = stereo(0) }},
			{"muted", func(n *pw.Node) { n.Mute = true }},
		} {
			start := now.Add(time.Duration(i) * time.Hour)
			setNode(m, 10, tc.set, start)
			m.refreshShown(start.Add(time.Minute))
			wantShown(t, m, 10, "FluidSynth %s hidden", tc.name)
			// Turned up again a while later, it gets the rule's after
			// before hiding.
			up := start.Add(5 * time.Minute)
			setNode(m, 10, func(n *pw.Node) { n.Channels, n.Mute = stereo(1), false }, up)
			wantShown(t, m, 10, "FluidSynth hidden as soon as turned up from %s", tc.name)
			m.refreshShown(up.Add(9 * time.Second))
			wantShown(t, m, 10, "FluidSynth hidden 9s after %s", tc.name)
			m.refreshShown(up.Add(10 * time.Second))
			wantHidden(t, m, 10, "FluidSynth shown 10s after %s", tc.name)
		}
		if inaudible(&pw.Node{Class: pw.Recording, Mute: true, Channels: stereo(1)}) {
			t.Error("a muted recording stream's meter reads silence, but it is measured before its mute")
		}
	})
	t.Run("no meter levels", func(t *testing.T) {
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("FluidSynth"), hideSilent, 10*time.Second))
		m.refreshShown(now)
		wantShown(t, m, 10, "playing FluidSynth hidden with the meter down")
		if !m.act.wakeAt.IsZero() {
			t.Errorf("wake at %v with nothing to wait for", m.act.wakeAt)
		}
		// The meter comes up and reads silence, then loses the stream again.
		up := now.Add(time.Minute)
		m.hear(&pulse.Snapshot{Peaks: map[uint32][]float64{10: {0, 0}}}, up)
		wantHidden(t, m, 10, "silent FluidSynth shown once metered")
		m.hear(&pulse.Snapshot{}, up.Add(time.Minute))
		wantShown(t, m, 10, "FluidSynth hidden after the meter lost it")
	})
	t.Run("idle", func(t *testing.T) {
		now := time.Now()
		m := activityMixer(defaultOptions(), rule(nameIs("FluidSynth"), hideSilent, 10*time.Second))
		m.refreshShown(now)
		m.hear(&pulse.Snapshot{Peaks: map[uint32][]float64{10: {0.5, 0.5}}}, now)
		wantShown(t, m, 10, "playing FluidSynth hidden")
		setState(m, 10, pw.StateIdle, now)
		if targets, _ := m.silentTargets(nil); len(targets) != 0 {
			t.Errorf("idle FluidSynth metered: %v", targets)
		}
		m.refreshShown(now.Add(10 * time.Second))
		wantHidden(t, m, 10, "idle FluidSynth shown after 10s")
	})
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
