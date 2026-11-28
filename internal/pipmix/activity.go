package pipmix

import (
	"maps"
	"slices"
	"time"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

// activeLinger is how long a stream or device stays on the Default tab
// after it stops being active, when it is only shown because it was active.
const activeLinger = 5 * time.Second

// A ruleNode identifies one hide rule applied to one node: the index of
// the rule in mixer.hide and the ID of the node.
type ruleNode struct {
	rule int
	id   uint32
}

// A nodeState is the per-node data that activity keeps for one node of the
// latest PipeWire snapshot. trackNodes rebuilds it on every new snapshot and
// copies the timestamps over from the node's previous state.
type nodeState struct {
	known   bool      // the node's volume and mute have been reported
	active  bool      // the node is active
	idleAt  time.Time // when the node went from active to not active; zero if that was never seen
	muted   bool      // the node is muted
	mutedAt time.Time // when the node went from unmuted to muted; zero if that was never seen
	quiet   bool      // the node is muted or at 0%, so its meter reads silence even while it plays
	gain    []float64 // per channel, the factor its volume scales levels by; nil if levels are measured before volume
	rules   []int     // indexes in mixer.hide of the hide rules that match the node

	silent  bool         // a silent rule matches the node and the node can be metered
	target  pulse.Target // what to meter for the node, if silent is set
	metered bool         // the latest meter snapshot has levels for the node
}

// activity holds the state used to decide which nodes the Default tab
// shows: which nodes are active, muted or silent, and since when.
type activity struct {
	snap  *pw.Snapshot         // the PipeWire snapshot that nodes was built from
	nodes map[uint32]nodeState // state of each listable node in snap, by node ID

	// heard records, for each silent rule and node it matches, the last time
	// the node's level was above the rule's threshold. A missing entry means
	// the node has not been above the threshold yet.
	heard     map[ruleNode]time.Time
	heardSnap *pulse.Snapshot // the last meter snapshot passed to hear

	shown  map[uint32]bool // nodes to show because they are active or stopped less than activeLinger ago
	hidden map[uint32]bool // nodes that a hide rule currently hides
	wakeAt time.Time       // the next time updateShown must run because a delay runs out; zero if none
}

// listable reports whether the Default tab can list n. It can't list the
// recording side of a JACK client. A JACK client with both inputs and outputs
// appears in the snapshot twice with the same ID, once as playback and once
// as recording, and rows, focus and activity are all keyed by ID. Only the
// Recording tab lists the recording side.
func listable(n *pw.Node) bool { return !(n.JACK && n.Class == pw.Recording) }

// trackNodes rebuilds m.act.nodes from the current PipeWire snapshot. It
// does nothing if the snapshot is the one it last saw. updateShown calls it.
func (m *mixer) trackNodes(now time.Time) {
	a := &m.act
	if a.snap == m.snap {
		return
	}
	a.snap = m.snap
	nodes := make(map[uint32]nodeState)
	for i := range m.snap.Nodes {
		n := &m.snap.Nodes[i]
		if !listable(n) {
			continue
		}
		prev := a.nodes[n.ID]
		st := nodeState{
			known:  len(n.Channels) > 0,
			active: n.Active,
			muted:  n.Mute && !n.JACK,
			quiet:  inaudible(n),
			gain:   volumeGain(n),
		}
		switch {
		case st.active:
		case prev.active:
			st.idleAt = now
		default:
			st.idleAt = prev.idleAt
		}
		// PipeWire reports a new node as unmuted until it has sent the node's
		// volume and mute. So mutedAt is only set if the node was unmuted in
		// a snapshot where its mute was already known. A node that is muted
		// when its mute first becomes known gets no mutedAt, and a muted rule
		// hides it without waiting.
		switch {
		case !st.muted:
		case prev.known && !prev.muted:
			st.mutedAt = now
		default:
			st.mutedAt = prev.mutedAt
		}
		if a.heardSnap != nil {
			_, st.metered = a.heardSnap.Peaks[n.ID]
		}
		for ri := range m.hide {
			r := &m.hide[ri]
			if !r.Match.matches(n) {
				continue
			}
			st.rules = append(st.rules, ri)
			if r.When != hideSilent {
				continue
			}
			if !st.silent {
				st.target, st.silent = meterTarget(m.snap, n)
			}
			// The node was muted or at 0% and now isn't. Count that as
			// heard now, so the rule waits its full delay before hiding it.
			if st.silent && prev.quiet && !st.quiet {
				a.hearAt(ruleNode{ri, n.ID}, now)
			}
		}
		nodes[n.ID] = st
	}
	a.nodes = nodes
	maps.DeleteFunc(a.heard, func(k ruleNode, _ time.Time) bool {
		st := nodes[k.id]
		return !st.silent || !slices.Contains(st.rules, k.rule)
	})
}

// hearAt records t as the last time the node of k was above the threshold
// of the rule of k.
func (a *activity) hearAt(k ruleNode, t time.Time) {
	if a.heard == nil {
		a.heard = make(map[ruleNode]time.Time)
	}
	a.heard[k] = t
}

// defaultShow returns the -default.recording, -default.outputs or
// -default.inputs setting that applies to n, or showAll for nodes that the
// Default tab always lists.
func (m *mixer) defaultShow(n *pw.Node) showWhen {
	switch {
	case n.Class == pw.Recording:
		return m.opts.recording
	case n.Class == pw.Sink && !n.Default:
		return m.opts.outputs
	case n.Class == pw.Source:
		return m.opts.inputs
	}
	return showAll
}

// updateShown takes in the current PipeWire snapshot if it is new, then
// recomputes m.act.shown and m.act.hidden for the time now, and reports
// whether either changed. It sets m.act.wakeAt to the earliest
// time at which the result can change without a new snapshot or meter level.
func (m *mixer) updateShown(now time.Time) (changed bool) {
	m.trackNodes(now)
	a := &m.act
	var next time.Time
	// until reports whether t is after now. If it is, t becomes the wake
	// time unless an earlier one is already set.
	until := func(t time.Time) bool {
		if !now.Before(t) {
			return false
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
		return true
	}
	// active reports whether the node is active or stopped being active less
	// than d ago.
	active := func(st nodeState, d time.Duration) bool {
		return st.active || !st.idleAt.IsZero() && until(st.idleAt.Add(d))
	}

	var shown, hidden map[uint32]bool
	for i := range m.snap.Nodes {
		n := &m.snap.Nodes[i]
		if !listable(n) {
			continue
		}
		st := a.nodes[n.ID]
		if m.defaultShow(n) == showActive && active(st, activeLinger) {
			if shown == nil {
				shown = make(map[uint32]bool)
			}
			shown[n.ID] = true
		}
		hide := false
		for _, ri := range st.rules {
			r := &m.hide[ri]
			switch r.When {
			case hideAlways:
				hide = true
			case hideIdle:
				hide = !active(st, r.after)
			case hideMuted:
				// With a zero mutedAt the delay has always run out, so the
				// node hides at once.
				hide = st.muted && !until(st.mutedAt.Add(r.after))
			case hideSilent:
				switch {
				case !st.silent: // the node can't be metered, so stays visible
				case st.quiet: // muted or at 0%: its meter says nothing about what it plays
				case st.active && !st.metered: // the meter has no levels for it yet
				default:
					// A node with no entry in heard gets the zero time, so
					// the delay has always run out and it hides at once.
					hide = !until(a.heard[ruleNode{ri, n.ID}].Add(r.after))
				}
			}
			if hide {
				break
			}
		}
		if hide {
			if hidden == nil {
				hidden = make(map[uint32]bool)
			}
			hidden[n.ID] = true
		}
	}
	a.wakeAt = next
	changed = !maps.Equal(shown, a.shown) || !maps.Equal(hidden, a.hidden)
	a.shown, a.hidden = shown, hidden
	return changed
}

// meteredAfterVolume reports whether n's levels are measured after its
// volume and mute apply: true for playback streams and input devices.
func meteredAfterVolume(n *pw.Node) bool {
	return !n.JACK && n.Class != pw.Sink && n.Class != pw.Recording
}

// volumeGain returns the factor that each channel's volume scales its level
// by, the cube of the volume, or nil if n's levels are measured before volume.
func volumeGain(n *pw.Node) []float64 {
	if !meteredAfterVolume(n) {
		return nil
	}
	gain := make([]float64, len(n.Channels))
	for i, c := range n.Channels {
		gain[i] = c.Volume * c.Volume * c.Volume
	}
	return gain
}

// inaudible reports whether n's meter reads silence because n is muted or
// has every channel at 0%, even while n plays.
func inaudible(n *pw.Node) bool {
	if !meteredAfterVolume(n) {
		return false
	}
	if n.Mute {
		return true
	}
	return len(n.Channels) > 0 && !slices.ContainsFunc(n.Channels, func(c pw.Channel) bool { return c.Volume > 0 })
}

// refreshShown calls updateShown. If the set of visible nodes changed, it
// asks for a redraw and updates focus, scrolling and the metered nodes.
func (m *mixer) refreshShown(now time.Time) {
	if !m.updateShown(now) {
		return
	}
	m.redraw = true
	m.refocus()
	m.watchMeters()
}

// hear processes a new meter snapshot for the silent rules. For each node
// that a silent rule matches, it records in m.act.heard when a peak is above
// the rule's threshold, and updates whether the meter has levels for the
// node. It calls refreshShown if that can change which nodes are visible.
// Peaks are compared as the level they have at a volume of 100%.
func (m *mixer) hear(snap *pulse.Snapshot, now time.Time) {
	a := &m.act
	if snap == a.heardSnap {
		return
	}
	a.heardSnap = snap
	woke := false
	for id, st := range a.nodes {
		if !st.silent {
			continue
		}
		peaks, ok := snap.Peaks[id]
		if ok != st.metered {
			st.metered = ok
			a.nodes[id] = st
			woke = true
		}
		for _, ri := range st.rules {
			r := &m.hide[ri]
			if r.When != hideSilent || !st.above(peaks, r.level) {
				continue
			}
			a.hearAt(ruleNode{ri, id}, now)
			woke = woke || a.hidden[id]
		}
	}
	if woke {
		m.refreshShown(now)
	}
}

// listenRate is how many peaks a second the meter streams send while no
// meter is drawn and only silent rules use the levels. Hide delays are
// counted in seconds, so a few peaks a second are enough, and they wake the
// process several times less often than the rate the meters are drawn at.
const listenRate = 4

// above reports whether any of peaks, scaled to a volume of 100%, is above
// level.
func (st nodeState) above(peaks []float64, level float64) bool {
	for ch, p := range peaks {
		if ch < len(st.gain) {
			if p > level*st.gain[ch] {
				return true
			}
		} else if p > level {
			return true
		}
	}
	return false
}

// silentTargets adds the active nodes that silent rules match to targets,
// the set of nodes to meter, and returns the result along with the IDs it
// added. Silent rules need levels for these nodes even when no meter for
// them is on screen: when they are hidden or scrolled out of view, or when
// the meters are paused because the terminal isn't focused. Nodes that
// aren't active are not metered, and silent rules treat them as silent.
//
// While no meter is drawn at all, the nodes it adds are metered at
// listenRate, or at -meter.rate if that is lower.
func (m *mixer) silentTargets(targets map[uint32]pulse.Target) (all map[uint32]pulse.Target, added map[uint32]bool) {
	if m.meter == nil {
		return targets, nil
	}
	rate := 0
	if m.metersOff() {
		rate = min(listenRate, m.opts.meterRate)
	}
	for id, st := range m.act.nodes {
		if !st.silent || !st.active {
			continue
		}
		if _, ok := targets[id]; ok {
			continue
		}
		if targets == nil {
			targets = make(map[uint32]pulse.Target)
		}
		if added == nil {
			added = make(map[uint32]bool)
		}
		t := st.target
		t.Rate = rate
		targets[id], added[id] = t, true
	}
	return targets, added
}
