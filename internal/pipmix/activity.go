package pipmix

import (
	"maps"
	"time"

	"github.com/some-programs/pipmix/internal/pw"
)

// activeLinger is how long a stream or device stays on the Default tab
// after it stops being active, when it is only shown because it was active.
const activeLinger = 5 * time.Second

// A nodeState is the per-node data that activity keeps for one node of the
// latest PipeWire snapshot. trackNodes rebuilds it on every new snapshot and
// copies the timestamps over from the node's previous state.
type nodeState struct {
	known   bool      // the node's volume and mute have been reported
	active  bool      // the node is active
	idleAt  time.Time // when the node went from active to not active; zero if that was never seen
	muted   bool      // the node is muted
	mutedAt time.Time // when the node went from unmuted to muted; zero if that was never seen
	rules   []int     // indexes in mixer.hide of the hide rules that match the node
}

// activity holds the state used to decide which nodes the Default tab
// shows: which nodes are active or muted, and since when.
type activity struct {
	snap  *pw.Snapshot         // the PipeWire snapshot that nodes was built from
	nodes map[uint32]nodeState // state of each listable node in snap, by node ID

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
		for ri := range m.hide {
			r := &m.hide[ri]
			if !r.Match.matches(n) {
				continue
			}
			st.rules = append(st.rules, ri)
		}
		nodes[n.ID] = st
	}
	a.nodes = nodes
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
// time at which the result can change without a new snapshot.
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
