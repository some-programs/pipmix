package pulse

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestPosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		i    int
		want byte
	}{
		{"MONO", 0, 0},
		{"FL", 0, 1},
		{"FR", 1, 2},
		{"LFE", 3, 7},
		{"SR", 5, 11},
		{"AUX0", 0, 12},
		{"AUX5", 0, 17},
		{"AUX31", 0, 43},
		{"UNKNOWN", 2, 14},
		{"AUX99", 1, 13},
	} {
		if got := position(tc.name, tc.i); got != tc.want {
			t.Errorf("position(%q, %d) = %d, want %d", tc.name, tc.i, got, tc.want)
		}
	}
}

func TestTagstruct(t *testing.T) {
	b := command(cmdSetClientName, 7)
	b.proplist([][2]string{{"a", "xy"}})
	want := []byte{
		'L', 0, 0, 0, 9, 'L', 0, 0, 0, 7,
		'P', 't', 'a', 0, 'L', 0, 0, 0, 3, 'x', 0, 0, 0, 3, 'x', 'y', 0, 'N',
	}
	if !bytes.Equal(b.b, want) {
		t.Errorf("proplist = %q, want %q", b.b, want)
	}

	b = &builder{}
	b.u32(5)
	b.sampleSpec(formatFloat32LE, 2, 25)
	r := &reader{b: b.b}
	if v := r.u32(); v != 5 {
		t.Errorf("u32 = %d, want 5", v)
	}
	if f, c, rate := r.sampleSpec(); f != formatFloat32LE || c != 2 || rate != 25 || r.err != nil {
		t.Errorf("sampleSpec = %d %d %d %v", f, c, rate, r.err)
	}
	r.u32()
	if r.err == nil {
		t.Error("reading past the end gives no error")
	}
	r = &reader{b: []byte{'t', 0, 0, 0, 1}}
	r.u32()
	if r.err == nil {
		t.Error("reading a u32 from a string's tag gives no error")
	}
}

// A fakeServer speaks enough of pipewire-pulse's protocol for a Meter: it
// answers the handshake and a record stream's creation, and the test sends
// the stream's peaks.
type fakeServer struct {
	t     *testing.T
	ln    net.Listener
	conns chan *fakeConn
	// idle is how many record streams' creations get no reply, as on a
	// node that doesn't run.
	idle atomic.Int32
	// unanswered gets the connections of those.
	unanswered chan *fakeConn
}

// A fakeConn is a client's connection that has made a stream.
type fakeConn struct {
	c        net.Conn
	channels int
	source   string // the source name asked for
	direct   uint32 // direct_on_input
	closed   chan struct{}
}

func newFakeServer(t *testing.T) *fakeServer {
	path := filepath.Join(t.TempDir(), "native")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PULSE_SERVER", "unix:"+path)
	s := &fakeServer{t: t, ln: ln, conns: make(chan *fakeConn, 16), unanswered: make(chan *fakeConn, 16)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func reply(tag uint32) *builder { return command(cmdReply, tag) }

func (s *fakeServer) serve(c net.Conn) {
	defer c.Close()
	fc := &fakeConn{c: c, closed: make(chan struct{})}
	defer close(fc.closed)
	pr := newPacketReader(c)
	for {
		p, _, err := pr.read()
		if err != nil {
			return
		}
		r := &reader{b: p.data}
		cmd, tag := r.u32(), r.u32()
		var b *builder
		switch cmd {
		case cmdAuth:
			b = reply(tag)
			b.u32(protocolVersion)
		case cmdSetClientName:
			b = reply(tag)
			b.u32(1)
		case cmdCreateRecordStream:
			_, ch, _ := r.sampleSpec()
			fc.channels = int(ch)
			// Skip the channel map and the source index to the name.
			r.take(2 + int(ch))
			r.u32()
			if r.take(1)[0] == tagString {
				i := bytes.IndexByte(r.b, 0)
				fc.source = string(r.b[:i])
				r.b = r.b[i+1:]
			}
			if i := bytes.LastIndex(r.b, []byte{tagStringNull, tagU32}); i >= 0 {
				fc.direct = binary.BigEndian.Uint32(r.b[i+2:])
			}
			if s.idle.Add(-1) >= 0 {
				s.unanswered <- fc
				continue
			}
			b = reply(tag)
			b.u32(3) // channel
			b.u32(9) // source output index
			b.u32(1 << 20)
			b.u32(4 * uint32(ch))
			b.sampleSpec(formatFloat32LE, ch, DefaultRate)
			if err := writePacket(c, b.b); err != nil {
				return
			}
			s.conns <- fc
			continue
		case cmdDeleteRecordStream:
			b = reply(tag)
		default:
			s.t.Errorf("fake server: command %d", cmd)
			return
		}
		if err := writePacket(c, b.b); err != nil {
			return
		}
	}
}

// peaks sends one frame of peaks on the stream's channel.
func (fc *fakeConn) peaks(t *testing.T, peaks ...float32) {
	t.Helper()
	if _, err := fc.c.Write(makePeakPacket(3, peaks...)); err != nil {
		t.Fatal(err)
	}
}

func (fc *fakeConn) kill(t *testing.T) {
	t.Helper()
	b := command(cmdRecordStreamKilled, math.MaxUint32)
	b.u32(3)
	if err := writePacket(fc.c, b.b); err != nil {
		t.Fatal(err)
	}
}

func (s *fakeServer) conn() *fakeConn {
	s.t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(5 * time.Second):
		s.t.Fatal("no stream made")
		return nil
	}
}

// waitLevels waits for a snapshot in which ok holds.
func waitLevels(t *testing.T, m *Meter, ok func(map[uint32][]float64) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if levels := m.Snapshot().Peaks; ok(levels) {
			return
		}
		select {
		case <-m.Updated():
		case <-deadline:
			t.Fatalf("levels %v", m.Snapshot().Peaks)
		}
	}
}

func TestMeter(t *testing.T) {
	s := newFakeServer(t)
	m := NewMeter(nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Watch(map[uint32]Target{
		1: {Kind: Sink, Name: "speakers", Channels: []string{"FL", "FR"}},
		2: {Kind: Stream, Serial: 77},
	})
	a, b := s.conn(), s.conn()
	if a.source == "" {
		a, b = b, a
	}
	if a.source != "speakers.monitor" || a.channels != 2 {
		t.Errorf("sink's stream records %q, %d channels; want speakers.monitor, 2", a.source, a.channels)
	}
	if b.direct != 77 || b.channels != 1 || b.source != "" {
		t.Errorf("stream's stream is on %d, %q, %d channels; want direct on 77, 1 channel", b.direct, b.source, b.channels)
	}

	// Each target's peaks, each on its own connection.
	a.peaks(t, 0.5, -0.25)
	b.peaks(t, 1)
	waitLevels(t, m, func(l map[uint32][]float64) bool {
		return slices.Equal(l[1], []float64{0.5, 0.25}) && slices.Equal(l[2], []float64{1})
	})

	// Silence after stale.
	waitLevels(t, m, func(l map[uint32][]float64) bool {
		return slices.Equal(l[1], []float64{0, 0}) && slices.Equal(l[2], []float64{0})
	})

	// A killed stream goes, and comes back after minRetry.
	b.kill(t)
	waitLevels(t, m, func(l map[uint32][]float64) bool { _, ok := l[2]; return !ok })
	b = s.conn()
	b.peaks(t, 0.75)
	waitLevels(t, m, func(l map[uint32][]float64) bool { return slices.Equal(l[2], []float64{0.75}) })

	// A target no longer watched has its connection closed.
	m.Watch(map[uint32]Target{2: {Kind: Stream, Serial: 77}})
	select {
	case <-a.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the sink's connection stays open")
	}
	waitLevels(t, m, func(l map[uint32][]float64) bool { _, ok := l[1]; return !ok })

	// So does a changed one's: it gets a new stream.
	m.Watch(map[uint32]Target{2: {Kind: Stream, Serial: 78}})
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the changed target's connection stays open")
	}
	c := s.conn()
	if c.direct != 78 {
		t.Errorf("new stream is on %d, want 78", c.direct)
	}

	// Run's end closes them all.
	cancel()
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a connection stays open after Run")
	}
}

// TestMeterIdle checks that a stream whose creation gets no reply, its
// node idle, is asked for again before pipewire-pulse drops it.
func TestMeterIdle(t *testing.T) {
	defer func(d time.Duration) { createWait = d }(createWait)
	createWait = 50 * time.Millisecond
	s := newFakeServer(t)
	s.idle.Store(2)
	m := NewMeter(nil, 0)
	ctx := t.Context()
	go m.Run(ctx)

	m.Watch(map[uint32]Target{1: {Kind: Sink, Name: "speakers", Channels: []string{"FL", "FR"}}})
	for range 2 {
		var fc *fakeConn
		select {
		case fc = <-s.unanswered:
		case <-time.After(5 * time.Second):
			t.Fatal("no stream asked for")
		}
		select {
		case <-fc.closed:
		case <-time.After(5 * time.Second):
			t.Fatal("an unanswered stream's connection stays open")
		}
	}
	// Asked again at once, not after a retry's wait.
	start := time.Now()
	c := s.conn()
	if d := time.Since(start); d > createWait+minRetry/2 {
		t.Errorf("asked again after %v", d)
	}
	c.peaks(t, 0.5, 0.5)
	waitLevels(t, m, func(l map[uint32][]float64) bool { return slices.Equal(l[1], []float64{0.5, 0.5}) })
}

func TestMeterNoServer(t *testing.T) {
	t.Setenv("PULSE_SERVER", "unix:"+filepath.Join(t.TempDir(), "none"))
	m := NewMeter(nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	m.Watch(map[uint32]Target{1: {Kind: Source, Name: "mic"}})
	time.Sleep(50 * time.Millisecond)
	if l := m.Snapshot().Peaks; len(l) != 0 {
		t.Errorf("levels without a server: %v", l)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run doesn't return")
	}
}

func TestMeterInterval(t *testing.T) {
	s := newFakeServer(t)
	m := NewMeter(nil, 0)
	ctx := t.Context()
	go m.Run(ctx)
	m.Watch(map[uint32]Target{1: {Kind: Source, Name: "mic"}})
	c := s.conn()

	// A peak between two snapshots isn't missed: the next has it, and
	// the one after the level it fell to.
	c.peaks(t, 0.125)
	waitLevels(t, m, func(l map[uint32][]float64) bool { return slices.Equal(l[1], []float64{0.125}) })
	start := time.Now()
	c.peaks(t, 0.875)
	time.Sleep(2 * time.Millisecond)
	c.peaks(t, 0.25)
	<-m.Updated()
	if got := m.Snapshot().Peaks[1]; !slices.Equal(got, []float64{0.875}) {
		t.Errorf("levels after a short peak %v, want [0.875]", got)
	}
	if d := time.Since(start); d < m.Interval()/2 {
		t.Errorf("snapshot after %v, want one per %v", d, m.Interval())
	}
	// The one after shows the level it fell to.
	<-m.Updated()
	if got := m.Snapshot().Peaks[1]; !slices.Equal(got, []float64{0.25}) {
		t.Errorf("levels after the peak %v, want [0.25]", got)
	}
}

func TestMeterAverage(t *testing.T) {
	s := newFakeServer(t)
	m := NewMeter(nil, 0)
	ctx := t.Context()
	go m.Run(ctx)
	m.Watch(map[uint32]Target{1: {Kind: Source, Name: "mic"}})
	c := s.conn()

	// A second of peaks of 0.5, in one packet of 25 mono frames: the
	// average has come within e^(-1/0.3s)/2 of 0.5's root mean square.
	frames := make([]float32, DefaultRate)
	for i := range frames {
		frames[i] = 0.5
	}
	c.peaks(t, frames...)
	alpha := 1 - math.Exp(-1/(DefaultRate*averageTime.Seconds()))
	want := 0.5 * math.Sqrt(1-math.Pow(1-alpha, DefaultRate))
	waitLevels(t, m, func(map[uint32][]float64) bool {
		a := m.Snapshot().Averages[1]
		return len(a) == 1 && math.Abs(a[0]-want) < 1e-9
	})
	if p := m.Snapshot().Peaks[1]; !slices.Equal(p, []float64{0.5}) {
		t.Errorf("peaks %v, want [0.5]", p)
	}
}

func TestMeterJACK(t *testing.T) {
	s := newFakeServer(t)
	m := NewMeter(nil, 0)
	m2 := NewMeter(nil, 0)
	if m2.Prefix == m.Prefix {
		t.Errorf("m2 Prefix %q == m Prefix %q", m2.Prefix, m.Prefix)
	}
	m.Prefix = "pipmix.meter."
	var readyName atomic.Value
	m.JACKReady = func(_ context.Context, name string) {
		readyName.Store(name)
	}
	ctx := t.Context()
	go m.Run(ctx)

	m.Watch(map[uint32]Target{
		5: {Kind: JACK, Serial: 137, Channels: []string{"out_1", "out_2"}},
	})
	c := s.conn()
	if c.source != "@DEFAULT_MONITOR@" || c.channels != 2 {
		t.Fatalf("JACK stream records %q, %d channels; want @DEFAULT_MONITOR@, 2", c.source, c.channels)
	}
	for range 100 {
		if readyName.Load() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, _ := readyName.Load().(string); got != "pipmix.meter.jack.137.1" {
		t.Fatalf("readyName = %q, want pipmix.meter.jack.137.1", got)
	}
	// First frame after flush is skipped; the next frame is published.
	c.peaks(t, 0.9, 0.9)
	time.Sleep(5 * time.Millisecond)
	c.peaks(t, 0.25, 0.75)
	waitLevels(t, m, func(l map[uint32][]float64) bool {
		return slices.Equal(l[5], []float64{0.25, 0.75})
	})
}

func makePeakPacket(channel uint32, peaks ...float32) []byte {
	b := make([]byte, descriptorSize, descriptorSize+4*len(peaks))
	binary.BigEndian.PutUint32(b[0:], uint32(4*len(peaks)))
	binary.BigEndian.PutUint32(b[4:], channel)
	for _, p := range peaks {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(p))
	}
	return b
}

func TestPacketReaderTimeoutAndCoalesce(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	pr := newPacketReader(c1)

	// Idle timeout before any byte arrives leaves framing intact.
	c1.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	_, idle, err := pr.read()
	if err == nil || !idle {
		t.Fatalf("read on idle pipe = (idle=%v, err=%v), want (true, timeout)", idle, err)
	}
	c1.SetReadDeadline(time.Time{})

	// Two coalesced packets arrive in a single write.
	pkt1 := makePeakPacket(3, 0.25, 0.5)
	pkt2 := makePeakPacket(3, 0.75, 1.0)
	go func() {
		_, _ = c2.Write(append(pkt1, pkt2...))
	}()

	p1, idle, err := pr.read()
	if err != nil || idle || p1.channel != 3 || len(p1.data) != 8 {
		t.Fatalf("first packet = (%+v, idle=%v, err=%v)", p1, idle, err)
	}
	if v := math.Float32frombits(binary.LittleEndian.Uint32(p1.data)); v != 0.25 {
		t.Fatalf("first sample = %v, want 0.25", v)
	}

	p2, idle, err := pr.read()
	if err != nil || idle || p2.channel != 3 || len(p2.data) != 8 {
		t.Fatalf("second packet = (%+v, idle=%v, err=%v)", p2, idle, err)
	}
	if v := math.Float32frombits(binary.LittleEndian.Uint32(p2.data)); v != 0.75 {
		t.Fatalf("second sample = %v, want 0.75", v)
	}
}

func TestMeterContinuousAndManyChannels(t *testing.T) {
	s := newFakeServer(t)
	m := NewMeter(nil, 0)
	ctx := t.Context()
	go m.Run(ctx)

	chNames := make([]string, inlineChannels+2)
	want := make([]float64, len(chNames))
	sendPeaks := make([]float32, len(chNames))
	for i := range chNames {
		chNames[i] = "FL"
		want[i] = 0.5
		sendPeaks[i] = 0.5
	}
	m.Watch(map[uint32]Target{
		1: {Kind: Sink, Name: "multi", Channels: chNames},
	})
	c := s.conn()
	if c.channels != len(chNames) {
		t.Fatalf("channels = %d, want %d", c.channels, len(chNames))
	}

	// Stream continuously past stale to ensure lazy deadlines extend without
	// spuriously zeroing the meter.
	c.peaks(t, sendPeaks...)
	waitLevels(t, m, func(l map[uint32][]float64) bool { return slices.Equal(l[1], want) })
	end := time.Now().Add(stale + 50*time.Millisecond)
	for time.Now().Before(end) {
		time.Sleep(25 * time.Millisecond)
		c.peaks(t, sendPeaks...)
		if got := m.Snapshot().Peaks[1]; !slices.Equal(got, want) {
			t.Fatalf("levels dropped during continuous stream: %v, want %v", got, want)
		}
	}
}
