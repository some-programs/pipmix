package pw

import (
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestNodeViewPropsCache(t *testing.T) {
	s := newSession(nil, nil, 1)
	n := &node{
		id:    10,
		class: Playback,
		props: map[string]string{propNodeName: "app", "client.id": "42"},
	}
	s.nodes[n.id] = n
	now := time.Now()

	if v := s.nodeView(n, now); v.Props[propApplicationProcessID] != "" {
		t.Fatalf("unexpected pid %q before client exists", v.Props[propApplicationProcessID])
	}

	// Adding client invalidates the cache and merges fallback keys without mutating n.props.
	s.global(42, ifaceClient, 3, map[string]string{
		propApplicationProcessID: "1234",
		propApplicationName:      "Firefox",
	})
	v := s.nodeView(n, now)
	if v.Props[propApplicationProcessID] != "1234" || v.Props[propApplicationName] != "Firefox" {
		t.Fatalf("merged props = %v, want pid=1234 app=Firefox", v.Props)
	}
	if n.props[propApplicationProcessID] != "" {
		t.Fatalf("n.props was mutated: %v", n.props)
	}
}

func TestConnFillCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var payload builder
	payload.structure(func() { payload.int(42) })
	var frame []byte
	frame = native.AppendUint32(frame, 7)
	frame = native.AppendUint32(frame, uint32(2)<<24|uint32(len(payload.buf)))
	frame = native.AppendUint32(frame, 1)
	frame = native.AppendUint32(frame, 0)
	frame = append(frame, payload.buf...)

	// Send enough messages in uneven chunks to force multiple compactions of an 8 KiB buffer.
	const count = 400
	go func() {
		sc, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer sc.Close()
		stream := make([]byte, 0, count*len(frame))
		for range count {
			stream = append(stream, frame...)
		}
		for len(stream) > 0 {
			n := min(len(stream), 777)
			if _, err := sc.Write(stream[:n]); err != nil {
				return
			}
			stream = stream[n:]
		}
	}()

	uc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	c := &conn{c: uc, buf: make([]byte, 8192), oob: make([]byte, 256)}
	for i := range count {
		m, err := c.read()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if f, err := m.payload.fields(); err != nil || f.int() != 42 || f.err != nil {
			t.Fatalf("read %d: bad payload (%v)", i, err)
		}
	}
	if len(c.buf) != 8192 {
		t.Fatalf("buf grew to %d bytes; expected compaction to reuse 8192-byte buffer", len(c.buf))
	}
}

func TestNodeInfoState(t *testing.T) {
	s := newSession(nil, nil, 1)
	p := &proxy{id: 5, kind: kindNode, global: 10}
	n := &node{id: 10, proxy: p.id, class: Recording}
	s.nodes[n.id] = n

	info := func(change int64, state State) {
		t.Helper()
		var b builder
		b.structure(func() {
			b.int(int32(n.id))
			b.int(0) // max input ports
			b.int(0) // max output ports
			b.long(change)
			b.int(0) // input ports
			b.int(0) // output ports
			b.int(int32(state))
			b.none() // error
			b.dict(nil)
			b.structure(func() { b.int(0) }) // params
		})
		pd, _, err := readPod(b.buf)
		if err != nil {
			t.Fatal(err)
		}
		f, err := pd.fields()
		if err != nil {
			t.Fatal(err)
		}
		s.dirty = false
		if err := s.nodeEvent(p, eventInfo, f); err != nil {
			t.Fatal(err)
		}
	}

	info(nodeChangeState, StateRunning)
	if v := s.nodeView(n, time.Now()); v.State != StateRunning || !s.dirty {
		t.Fatalf("after running: state %v, dirty %v; want running, true", v.State, s.dirty)
	}
	info(0, StateIdle) // state not in the change mask
	if n.state != StateRunning || s.dirty {
		t.Fatalf("unchanged mask: state %v, dirty %v; want running, false", n.state, s.dirty)
	}
	info(nodeChangeState, StateRunning) // same state again
	if s.dirty {
		t.Fatal("repeated state marked the session dirty")
	}
	info(nodeChangeState, StateSuspended)
	if n.state != StateSuspended || !s.dirty {
		t.Fatalf("after suspend: state %v, dirty %v; want suspended, true", n.state, s.dirty)
	}
}

func TestActiveDevices(t *testing.T) {
	s := newSession(nil, nil, 1)
	add := func(id uint32, class Class, state State, props map[string]string) *node {
		n := &node{id: id, class: class, state: state, props: props}
		s.nodes[id] = n
		return n
	}
	sink := add(1, Sink, StateRunning, nil)
	add(2, Recording, StateRunning, nil).meter = true                             // pipmix's meter
	add(3, Recording, StateRunning, map[string]string{propStreamMonitor: "true"}) // another mixer's
	firefox := add(4, Playback, StateIdle, nil)                                   // paused
	add(5, Source, StateRunning, nil)
	s.links[10] = link{out: 1, in: 2}
	s.links[11] = link{out: 1, in: 3}
	s.links[12] = link{out: 4, in: 1}

	active := func() bool {
		snap := s.snapshot(time.Now())
		for _, n := range snap.Nodes {
			if n.ID == 1 {
				return n.Active
			}
		}
		t.Fatal("sink not in snapshot")
		return false
	}
	if active() {
		t.Error("sink active with only meters and a paused stream linked")
	}
	firefox.state = StateRunning
	if !active() {
		t.Error("sink inactive with a running stream linked")
	}
	sink.state = StateSuspended
	if active() {
		t.Error("suspended sink active")
	}
	sink.state = StateRunning
	delete(s.links, 12)
	s.links[13] = link{out: 99, in: 1} // a node pipmix doesn't track
	if !active() {
		t.Error("sink inactive with an untracked node linked")
	}
}

func TestStreamLinks(t *testing.T) {
	s := newSession(nil, nil, 1)
	for _, n := range []*node{
		{id: 1, class: Source}, {id: 2, class: Sink}, {id: 3, class: Recording},
		{id: 4, class: Recording, meter: true},
		{id: 5, class: Playback},
	} {
		s.nodes[n.id] = n
	}
	s.links[10] = link{out: 1, in: 3} // FL
	s.links[11] = link{out: 1, in: 3} // FR
	s.links[12] = link{out: 2, in: 3}
	s.links[13] = link{out: 1, in: 4} // a meter
	s.links[14] = link{out: 5, in: 2}
	s.links[15] = link{out: 5, in: 4} // a meter
	for _, n := range s.snapshot(time.Now()).Nodes {
		if n.ID == 3 && !slices.Equal(n.From, []uint32{1, 2}) {
			t.Errorf("recording 3 from %v, want [1 2]", n.From)
		}
		if n.ID != 3 && n.From != nil {
			t.Errorf("node %d from %v, want none", n.ID, n.From)
		}
		if n.ID == 5 && !slices.Equal(n.To, []uint32{2}) {
			t.Errorf("playback 5 to %v, want [2]", n.To)
		}
	}
}

// TestLinkDirty checks which new links change the snapshot: those that bear
// on whether a device is active, or on what a stream plays to or records
// from.
func TestLinkDirty(t *testing.T) {
	s := newSession(nil, nil, 1)
	for _, n := range []*node{
		{id: 1, class: Sink}, {id: 2, class: Source},
		{id: 3, class: Recording}, {id: 5, class: Playback},
		{id: 4, class: Recording, meter: true},                                         // pipmix's meter
		{id: 6, class: Recording, props: map[string]string{propStreamMonitor: "true"}}, // another mixer's
	} {
		s.nodes[n.id] = n
	}
	for _, tc := range []struct {
		name    string
		out, in uint32
		want    bool
	}{
		{"stream to device", 5, 1, true},
		{"device to stream", 2, 3, true},
		{"stream to stream", 5, 3, true},
		{"untracked to device", 99, 1, true},
		{"stream to untracked", 5, 99, true},
		{"untracked to stream", 99, 3, true},
		{"device to meter", 1, 4, false},
		{"device to another mixer's meter", 1, 6, false},
		{"stream to meter", 5, 4, false},
		{"untracked to untracked", 98, 99, false},
	} {
		s.dirty = false
		s.global(100, ifaceLink, 3, map[string]string{
			"link.output.node": strconv.Itoa(int(tc.out)),
			"link.input.node":  strconv.Itoa(int(tc.in)),
		})
		if s.dirty != tc.want {
			t.Errorf("%s: dirty %v, want %v", tc.name, s.dirty, tc.want)
		}
	}
}
