package pipmix

import (
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

const (
	fadeIn  = 90 * time.Millisecond
	settled = 0.002
)

type smoothing struct {
	name            string
	attack, release time.Duration
}

var smoothings = []smoothing{
	{"fastest", 3 * time.Millisecond, 25 * time.Millisecond},
	{"faster", 5 * time.Millisecond, 50 * time.Millisecond},
	{"normal", 10 * time.Millisecond, 100 * time.Millisecond},
	{"slower", 15 * time.Millisecond, 200 * time.Millisecond},
	{"slowest", 30 * time.Millisecond, 300 * time.Millisecond},
}

const defaultSmoothing = 2

type meterMode int

const (
	modeAveragePeak meterMode = iota
	modeAverage
	modePeak
)

type meterLook int

const (
	meterSolid meterLook = iota
	meterShade
	meterDot
	meterOff
)

var meterLookFlags = []string{"solid", "shade", "dot"}

type (
	meterMsg     struct{}
	meterTickMsg struct{}
)

// waitMeter waits for the next meter snapshot, unless a wait is already
// pending.
func (m *mixer) waitMeter() tea.Cmd {
	if m.meter == nil || m.meterWaiting {
		return nil
	}
	m.meterWaiting = true
	return m.nextMeter()
}

func (m *mixer) nextMeter() tea.Cmd {
	if m.meter == nil {
		return nil
	}
	updated := m.meter.Updated()
	return func() tea.Msg {
		<-updated
		return meterMsg{}
	}
}

// meterTargets returns the visible nodes that can be metered.
func (m *mixer) meterTargets() map[uint32]pulse.Target {
	if m.help || m.blurred && m.opts.meters != metersAlways || m.metersHidden {
		return nil
	}
	items := m.items(m.tab)
	h := m.listHeight()
	total := 0
	for _, it := range items {
		total += m.itemHeight(it)
	}
	off := min(m.offset[m.tab], max(total-h, 0))
	targets := make(map[uint32]pulse.Target)
	y := 0
	for _, it := range items {
		top := y
		y += m.itemHeight(it)
		if y <= off || top >= off+h {
			continue
		}
		if t, ok := meterTarget(it.node); ok {
			targets[it.id] = t
		}
	}
	return targets
}

func meterTarget(n *pw.Node) (pulse.Target, bool) {
	if n == nil || len(n.Channels) == 0 {
		return pulse.Target{}, false
	}
	var names []string
	for _, c := range n.Channels {
		names = append(names, c.Name)
	}
	t := pulse.Target{Name: n.Name, Channels: names}
	switch {
	case n.JACK && n.Class == pw.Playback:
		t.Kind, t.Serial, t.Name = pulse.JACK, n.ID, ""
	case n.JACK:
		return pulse.Target{}, false
	case n.Class == pw.Sink:
		t.Kind = pulse.Sink
	case n.Class == pw.Source:
		t.Kind = pulse.Source
	case n.Class == pw.Playback:
		serial, err := strconv.ParseUint(n.Props["object.serial"], 10, 32)
		if err != nil {
			return pulse.Target{}, false
		}
		t.Kind, t.Serial, t.Name = pulse.Stream, uint32(serial), ""
	default:
		return pulse.Target{}, false
	}
	return t, true
}

func (m *mixer) watchMeters() {
	if m.meter == nil {
		return
	}
	targets := m.meterTargets()
	if maps.EqualFunc(targets, m.watched, pulse.Target.Equal) {
		return
	}
	m.watched = targets
	m.meter.Watch(targets)
}

func (m *mixer) startMetering() tea.Cmd {
	if m.ticking {
		return nil
	}
	snap := &pulse.Snapshot{}
	if m.meter != nil {
		snap = m.meter.Snapshot()
	}
	if !m.setLevels(snap, time.Now()) {
		return m.waitMeter()
	}
	m.ticking = true
	return tea.Tick(time.Second/time.Duration(m.opts.fps), func(time.Time) tea.Msg { return meterTickMsg{} })
}

func (m *mixer) tickMetering() tea.Cmd {
	snap := &pulse.Snapshot{}
	if m.meter != nil {
		snap = m.meter.Snapshot()
	}
	if !m.setLevels(snap, time.Now()) {
		m.ticking = false
		return m.waitMeter()
	}
	return tea.Tick(time.Second/time.Duration(m.opts.fps), func(time.Time) tea.Msg { return meterTickMsg{} })
}

// setLevels updates the fill and peak positions at now and reports whether
// any meter is still moving.
func (m *mixer) setLevels(snap *pulse.Snapshot, now time.Time) (moving bool) {
	dt := now.Sub(m.levelsAt).Seconds()
	m.levelsAt = now
	source := snap.Averages
	if m.meterMode == modePeak {
		source = snap.Peaks
	}
	sm := smoothings[m.smoothing]
	attack := 1 - math.Exp(-dt/sm.attack.Seconds())
	release := 1 - math.Exp(-dt/sm.release.Seconds())
	var buf [32]float64

	shown := func(id uint32) bool { return m.meterNode(id, source[id]) != nil }
	m.fill, m.fillStart = prune(m.fill, shown), prune(m.fillStart, shown)
	for id, raw := range source {
		n := m.meterNode(id, raw)
		if n == nil {
			continue
		}
		start, ok := m.fillStart[id]
		if !ok {
			start = now
			m.fillStart[id] = start
		}
		if now.Sub(start) < fadeIn {
			moving = true
		}
		target, v := scale(n, raw, buf[:0]), m.fill[id]
		if len(v) != len(target) {
			m.fill[id] = slices.Clone(target)
			continue
		}
		for ch, t := range target {
			k := release
			if t > v[ch] {
				k = attack
			}
			v[ch] += k * (t - v[ch])
			if math.Abs(t-v[ch]) < settled {
				v[ch] = t
			} else {
				moving = true
			}
		}
	}

	shown = func(id uint32) bool { return m.meterNode(id, snap.Peaks[id]) != nil }
	m.dot, m.dotHold = prune(m.dot, shown), prune(m.dotHold, shown)
	for id, raw := range snap.Peaks {
		n := m.meterNode(id, raw)
		if n == nil {
			continue
		}
		target, v, hold := scale(n, raw, buf[:0]), m.dot[id], m.dotHold[id]
		if len(v) != len(target) || len(hold) != len(target) {
			// Zero rises to the target and holds it.
			v, hold = make([]float64, len(target)), make([]time.Time, len(target))
			m.dot[id], m.dotHold[id] = v, hold
		}
		for ch, t := range target {
			switch {
			case t >= v[ch]:
				v[ch], hold[ch] = t, now.Add(m.opts.peakHold)
			case now.Before(hold[ch]):
				// Hold the peak.
			default:
				v[ch] = max(t, v[ch]-m.opts.limit*dt)
			}
			if v[ch] > t {
				moving = true
			}
		}
	}
	return moving
}

// meterNode returns the node that raw levels for id belong to, or nil if
// they are not shown.
func (m *mixer) meterNode(id uint32, raw []float64) *pw.Node {
	for i := range m.snap.Nodes {
		if n := &m.snap.Nodes[i]; n.ID == id && len(n.Channels) > 0 {
			if len(raw) != len(n.Channels) {
				return nil
			}
			return n
		}
	}
	return nil
}

// scale appends raw levels of n to dst on the cubic volume scale, applying
// sink volume and mute state.
func scale(n *pw.Node, raw, dst []float64) []float64 {
	for ch, l := range raw {
		v := math.Cbrt(l)
		if n.Class == pw.Sink {
			v *= n.Channels[ch].Volume
		}
		if n.Mute {
			v = 0
		}
		dst = append(dst, v)
	}
	return dst
}

// prune deletes the entries of levels whose id is not kept, making levels
// if it is nil.
func prune[V any](levels map[uint32]V, keep func(id uint32) bool) map[uint32]V {
	if levels == nil {
		return make(map[uint32]V)
	}
	maps.DeleteFunc(levels, func(id uint32, _ V) bool { return !keep(id) })
	return levels
}

func (m *mixer) level(it item, ch int) float64 {
	return m.fade(it.id) * channelLevel(m.fill[it.id], ch)
}

func (m *mixer) fade(id uint32) float64 {
	start, ok := m.fillStart[id]
	if !ok {
		return 1
	}
	p := min(m.levelsAt.Sub(start).Seconds()/fadeIn.Seconds(), 1)
	return p * p * (3 - 2*p)
}

func (m *mixer) peak(it item, ch int) float64 {
	if m.meterMode != modeAveragePeak {
		return 0
	}
	return m.fade(it.id) * channelLevel(m.dot[it.id], ch)
}

func channelLevel(levels []float64, ch int) float64 {
	if ch < 0 {
		v := 0.0
		for _, l := range levels {
			v = max(v, l)
		}
		return v
	}
	if ch < len(levels) {
		return levels[ch]
	}
	return 0
}

func (m *mixer) toggleMeterAlways() tea.Cmd {
	switch m.opts.meters {
	case metersOff:
		return notify("Meters are off (-meter.disable)")
	case metersAlways:
		m.opts.meters = metersFocused
		return notify("Meters only while the terminal is focused")
	}
	m.opts.meters = metersAlways
	return notify("Meters always, focused or not")
}

func (m *mixer) toggleMeters() tea.Cmd {
	if m.meter == nil {
		return notify("Meters are off (-meter.disable)")
	}
	m.metersHidden = !m.metersHidden
	if m.metersHidden {
		return notify("Meters off")
	}
	return notify("Meters on")
}

func (m *mixer) look() meterLook {
	if m.metersHidden || m.meter == nil {
		return meterOff
	}
	return m.opts.meterLook
}
