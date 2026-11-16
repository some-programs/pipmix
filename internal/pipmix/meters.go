package pipmix

import (
	"maps"
	"math"
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

type meterMsg struct{}
type meterTickMsg struct{}

func (m *mixer) waitMeter() tea.Cmd {
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
	if n == nil || n.JACK || len(n.Channels) == 0 {
		return pulse.Target{}, false
	}
	var names []string
	for _, c := range n.Channels {
		names = append(names, c.Name)
	}
	t := pulse.Target{Name: n.Name, Channels: names}
	switch n.Class {
	case pw.Sink:
		t.Kind = pulse.Sink
	case pw.Source:
		t.Kind = pulse.Source
	case pw.Playback:
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

func (m *mixer) updateLevels() tea.Cmd {
	snap := &pulse.Snapshot{}
	if m.meter != nil {
		snap = m.meter.Snapshot()
	}
	if !m.setLevels(snap, time.Now()) || m.ticking {
		return nil
	}
	m.ticking = true
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

	fill := make(map[uint32][]float64)
	for id, target := range m.shown(source) {
		prev := m.fill[id]
		v := make([]float64, len(target))
		for ch, t := range target {
			if ch >= len(prev) {
				v[ch] = t
				continue
			}
			k := release
			if t > prev[ch] {
				k = attack
			}
			v[ch] = prev[ch] + k*(t-prev[ch])
			if math.Abs(t-v[ch]) < settled {
				v[ch] = t
			} else {
				moving = true
			}
		}
		fill[id] = v
	}
	m.fill = fill

	starts := make(map[uint32]time.Time)
	for id := range fill {
		start, ok := m.fillStart[id]
		if !ok {
			start = now
		}
		starts[id] = start
		if now.Sub(start) < fadeIn {
			moving = true
		}
	}
	m.fillStart = starts

	dot := make(map[uint32][]float64)
	holds := make(map[uint32][]time.Time)
	for id, target := range m.shown(snap.Peaks) {
		prev, prevHold := m.dot[id], m.dotHold[id]
		v := make([]float64, len(target))
		hold := make([]time.Time, len(target))
		for ch, t := range target {
			switch {
			case ch >= len(prev) || ch >= len(prevHold) || t >= prev[ch]:
				v[ch], hold[ch] = t, now.Add(m.opts.peakHold)
			case now.Before(prevHold[ch]):
				v[ch], hold[ch] = prev[ch], prevHold[ch]
			default:
				v[ch], hold[ch] = max(t, prev[ch]-m.opts.limit*dt), prevHold[ch]
			}
			if v[ch] > t {
				moving = true
			}
		}
		dot[id], holds[id] = v, hold
	}
	m.dot, m.dotHold = dot, holds
	return moving
}

// shown scales raw levels to the cubic volume scale, applying sink volume and
// mute state.
func (m *mixer) shown(raw map[uint32][]float64) map[uint32][]float64 {
	nodes := make(map[uint32]*pw.Node)
	for i := range m.snap.Nodes {
		nodes[m.snap.Nodes[i].ID] = &m.snap.Nodes[i]
	}
	shown := make(map[uint32][]float64)
	for id, levels := range raw {
		n := nodes[id]
		if n == nil || len(levels) != len(n.Channels) {
			continue
		}
		v := make([]float64, len(levels))
		for ch, l := range levels {
			v[ch] = math.Cbrt(l)
			if n.Class == pw.Sink {
				v[ch] *= n.Channels[ch].Volume
			}
			if n.Mute {
				v[ch] = 0
			}
		}
		shown[id] = v
	}
	return shown
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
