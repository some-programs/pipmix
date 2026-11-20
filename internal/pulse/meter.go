// Package pulse measures audio peak and average levels via pipewire-pulse.
package pulse

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"slices"
	"sync/atomic"
	"time"
)

const (
	DefaultRate  = 40
	averageTime  = 100 * time.Millisecond
	minVersion   = 22
	ioTimeout    = 2 * time.Second
	stale        = 250 * time.Millisecond
	publishDelay = 10 * time.Millisecond
	minRetry     = time.Second
	maxRetry     = 30 * time.Second
)

var createWait = 30 * time.Second

// A Kind is the kind of node a Target measures.
type Kind int

const (
	Sink Kind = iota
	Source
	Stream
	JACK
)

// A Target is a node to measure.
type Target struct {
	Kind     Kind
	Name     string // node.name of a Sink or Source
	Serial   uint32 // object.serial of a Stream, or node ID of a JACK client
	Channels []string
}

func (t Target) String() string {
	switch t.Kind {
	case Sink:
		return "sink " + t.Name
	case Source:
		return "source " + t.Name
	case JACK:
		return fmt.Sprintf("jack node %d", t.Serial)
	}
	return fmt.Sprintf("stream serial %d", t.Serial)
}

func (t Target) Equal(u Target) bool {
	return t.Kind == u.Kind && t.Name == u.Name && t.Serial == u.Serial && slices.Equal(t.Channels, u.Channels)
}

// A Snapshot is the levels at one moment.
type Snapshot struct {
	Peaks    map[uint32][]float64
	Averages map[uint32][]float64
}

var meterInst atomic.Uint32 // distinguishes meter instances within the same process

// A Meter measures the levels of a set of targets.
type Meter struct {
	// Prefix is the prefix used for naming PipeWire nodes of meter streams.
	Prefix string

	// JACKReady is invoked with the meter stream's PipeWire node.name once a
	// JACK record stream is created, so its non-JACK links can be removed
	// before peaks are read.
	JACKReady func(ctx context.Context, name string)

	log     *log.Logger
	rate    int
	want    chan map[uint32]Target
	snap    atomic.Pointer[Snapshot]
	updated chan struct{}
	seq     atomic.Uint32 // ensures unique node.name per stream across concurrent workers
}

// NewMeter returns a meter whose streams send rate peaks per second
// (DefaultRate if rate <= 0).
func NewMeter(logger *log.Logger, rate int) *Meter {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	if rate <= 0 {
		rate = DefaultRate
	}
	inst := meterInst.Add(1)
	m := &Meter{
		Prefix:  fmt.Sprintf("pipmix.meter.%d.%d.", os.Getpid(), inst),
		log:     logger,
		rate:    rate,
		want:    make(chan map[uint32]Target, 1),
		updated: make(chan struct{}, 1),
	}
	m.snap.Store(&Snapshot{})
	return m
}

// Interval returns the nominal interval between peak frames.
func (m *Meter) Interval() time.Duration { return time.Second / time.Duration(m.rate) }

// Snapshot returns the latest levels.
func (m *Meter) Snapshot() *Snapshot { return m.snap.Load() }

// Updated returns a channel that receives when a new snapshot is published.
func (m *Meter) Updated() <-chan struct{} { return m.updated }

// Watch sets the targets to measure, replacing the previous set.
func (m *Meter) Watch(targets map[uint32]Target) {
	for {
		select {
		case m.want <- targets:
			return
		default:
		}
		select {
		case <-m.want:
		default:
		}
	}
}

// A worker measures one target.
type worker struct {
	target Target
	cancel context.CancelFunc
}

// inlineChannels is the channel count carried inline in a level message
// without heap allocation.
const inlineChannels = 8

// A level is a worker's latest peaks and averages; n < 0 when it has none.
type level struct {
	w        *worker
	key      uint32
	n        int
	peaksBuf [inlineChannels]float64
	avgsBuf  [inlineChannels]float64
	peaks    []float64
	avgs     []float64
}

func (l *level) slices() (peaks, avgs []float64) {
	if l.n <= inlineChannels {
		return l.peaksBuf[:l.n], l.avgsBuf[:l.n]
	}
	return l.peaks, l.avgs
}

// meterState holds a target's latest peaks, highest peaks since the last
// snapshot, and latest averages, all owned by Meter.Run.
type meterState struct {
	latest []float64
	held   []float64
	avgs   []float64
}

// Run measures until ctx is done.
func (m *Meter) Run(ctx context.Context) {
	workers := make(map[uint32]*worker)
	states := make(map[uint32]*meterState)
	updates := make(chan level, 64)
	var published time.Time
	flush := time.NewTimer(time.Hour)
	flush.Stop()
	defer flush.Stop()
	flushing := false
	delay := func() time.Duration { return max(publishDelay, m.Interval()-time.Since(published)) }
	for {
		dirty := false
		select {
		case <-ctx.Done():
			for _, w := range workers {
				w.cancel()
			}
			return
		case targets := <-m.want:
			for key, w := range workers {
				if t, ok := targets[key]; !ok || !t.Equal(w.target) {
					m.log.Printf("pulse: %v: unwatched", w.target)
					w.cancel()
					delete(workers, key)
					if _, ok := states[key]; ok {
						delete(states, key)
						dirty = true
					}
				}
			}
			for key, t := range targets {
				if workers[key] == nil {
					wctx, cancel := context.WithCancel(ctx)
					w := &worker{target: t, cancel: cancel}
					workers[key] = w
					m.log.Printf("pulse: %v: watched as %d", t, key)
					go m.measure(wctx, w, key, updates)
				}
			}
		case l := <-updates:
			if workers[l.key] != l.w {
				break // from a worker since cancelled
			}
			if l.n < 0 {
				delete(states, l.key)
			} else {
				peaks, avg := l.slices()
				st := states[l.key]
				if st == nil || len(st.latest) != len(peaks) {
					buf := make([]float64, 3*len(peaks))
					st = &meterState{
						latest: buf[:len(peaks)],
						held:   buf[len(peaks) : 2*len(peaks)],
						avgs:   buf[2*len(peaks):],
					}
					states[l.key] = st
				}
				copy(st.latest, peaks)
				for i, v := range peaks {
					st.held[i] = max(st.held[i], v)
				}
				copy(st.avgs, avg)
			}
			dirty = true
		case <-flush.C:
			flushing = false
			published = time.Now()
			snap := &Snapshot{}
			if n := len(states); n > 0 {
				total := 0
				for _, st := range states {
					total += len(st.held)
				}
				vals := make([]float64, 2*total)
				peaks := make(map[uint32][]float64, n)
				avgs := make(map[uint32][]float64, n)
				off := 0
				for key, st := range states {
					k := len(st.held)
					p := vals[off : off+k : off+k]
					copy(p, st.held)
					off += k
					a := vals[off : off+k : off+k]
					copy(a, st.avgs)
					off += k
					peaks[key] = p
					avgs[key] = a
					// A level that fell while held shows in the next snapshot.
					if !slices.Equal(st.held, st.latest) {
						dirty = true
						copy(st.held, st.latest)
					}
				}
				snap.Peaks = peaks
				snap.Averages = avgs
			}
			m.snap.Store(snap)
			select {
			case m.updated <- struct{}{}:
			default:
			}
		}
		if dirty && !flushing {
			flush.Reset(delay())
			flushing = true
		}
	}
}

// measure runs w's streams until ctx is done, waiting longer after each
// that fails before it makes another, and sends their peaks to updates.
func (m *Meter) measure(ctx context.Context, w *worker, key uint32, updates chan<- level) {
	send := func(peaks, avgs []float64) bool {
		l := level{w: w, key: key, n: -1}
		if peaks != nil {
			l.n = len(peaks)
			if len(peaks) <= inlineChannels {
				copy(l.peaksBuf[:], peaks)
				copy(l.avgsBuf[:], avgs)
			} else {
				l.peaks = slices.Clone(peaks)
				l.avgs = slices.Clone(avgs)
			}
		}
		select {
		case updates <- l:
			return true
		case <-ctx.Done():
			return false
		}
	}
	delay := minRetry
	for {
		ran, err := m.stream(ctx, w.target, send)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errIdle) {
			m.log.Printf("pulse: %v: no reply in %v, the node isn't running; asking again", w.target, createWait)
			continue
		}
		if ran {
			delay = minRetry
		}
		m.log.Printf("pulse: %v: %v; retrying in %v", w.target, err, delay)
		if !send(nil, nil) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, maxRetry)
	}
}

var (
	// errKilled says the server ended a stream: its node went, say.
	errKilled = errors.New("stream killed")
	// errIdle says a stream's creation got no reply in createWait.
	errIdle = errors.New("no reply to the stream's creation")
)

// stream connects, makes a record stream with peak detection on t and
// sends its peaks and their averages until the stream or the connection
// fails or ctx is done. It reports whether peaks came.
func (m *Meter) stream(ctx context.Context, t Target, send func(peaks, avgs []float64) bool) (ran bool, err error) {
	c, err := dial(ctx)
	if err != nil {
		return false, err
	}
	nc := c.c
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	var nodeName string
	switch t.Kind {
	case JACK:
		nodeName = fmt.Sprintf("%sjack.%d.%d", m.Prefix, t.Serial, m.seq.Add(1))
	case Sink:
		nodeName = fmt.Sprintf("%ssink.%d", m.Prefix, m.seq.Add(1))
	case Source:
		nodeName = fmt.Sprintf("%ssource.%d", m.Prefix, m.seq.Add(1))
	case Stream:
		nodeName = fmt.Sprintf("%sstream.%d.%d", m.Prefix, t.Serial, m.seq.Add(1))
	}
	nc.SetReadDeadline(time.Now().Add(createWait))
	m.log.Printf("pulse: %v: creating a record stream; the reply waits for the node to run", t)
	asked := time.Now()
	index, channels, err := c.create(t, m.rate, nodeName)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false, errIdle
	}
	if err != nil {
		return false, err
	}
	if t.Kind == JACK && m.JACKReady != nil {
		m.JACKReady(ctx, nodeName)
	}
	nc.SetDeadline(time.Time{})
	m.log.Printf("pulse: %v: record stream %d created after %v", t, index, time.Since(asked).Round(time.Millisecond))

	frame := 4 * channels
	ms := make([]float64, channels)
	peaks := make([]float64, channels)
	avgs := make([]float64, channels)
	last := make([]float64, 0, channels)
	lastAvgs := make([]float64, 0, channels)
	alpha := 1 - math.Exp(-1/(float64(m.rate)*averageTime.Seconds()))
	timeout := max(stale, 3*m.Interval())
	var lastAt time.Time
	armed := false
	arm := func(now time.Time) {
		lastAt = now
		if !armed {
			nc.SetReadDeadline(now.Add(timeout))
			armed = true
		}
	}
	silent, since := true, time.Now()
	skip := t.Kind == JACK
	if skip {
		arm(time.Now())
	}
	for {
		p, idle, err := c.pr.read()
		if idle {
			now := time.Now()
			if next := lastAt.Add(timeout); now.Before(next) {
				nc.SetReadDeadline(next)
				continue
			}
			skip, armed = false, false
			nc.SetReadDeadline(time.Time{})
			silent, since = true, now
			m.log.Printf("pulse: %v: no peaks for %v; showing 0 until they come", t, timeout)
			clear(ms)
			last, lastAvgs = last[:channels], lastAvgs[:channels]
			clear(last)
			clear(lastAvgs)
			if !send(last, lastAvgs) {
				return ran, ctx.Err()
			}
			continue
		}
		if err != nil {
			return ran, err
		}
		if p.channel == controlChannel {
			r := &reader{b: p.data}
			cmd := r.u32()
			if cmd == cmdRecordStreamKilled {
				return ran, errKilled
			}
			m.log.Printf("pulse: %v: command %d ignored", t, cmd)
			continue
		}
		if len(p.data) < frame {
			continue
		}
		if skip {
			skip = false
			arm(time.Now())
			continue
		}
		// Usually one frame; if more came, each channel's largest.
		clear(peaks)
		for f := 0; f+frame <= len(p.data); f += frame {
			for ch := range channels {
				v := math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(p.data[f+4*ch:]))))
				peaks[ch] = max(peaks[ch], v)
				ms[ch] += alpha * (v*v - ms[ch])
			}
		}
		for ch := range avgs {
			avgs[ch] = math.Sqrt(ms[ch])
		}
		now := time.Now()
		if silent {
			m.log.Printf("pulse: %v: peaks after %v", t, now.Sub(since).Round(time.Millisecond))
			silent = false
		}
		ran = true
		arm(now)
		if !slices.Equal(peaks, last) || !slices.Equal(avgs, lastAvgs) {
			last = append(last[:0], peaks...)
			lastAvgs = append(lastAvgs[:0], avgs...)
			if !send(peaks, avgs) {
				return ran, ctx.Err()
			}
		}
	}
}

// A conn is a connection to the server, while it's set up.
type conn struct {
	c   net.Conn
	pr  *packetReader
	tag uint32
}

// command starts a command with the next tag.
func (c *conn) command(cmd uint32) *builder {
	c.tag++
	return command(cmd, c.tag)
}

// reply reads the reply to the command with tag, skipping other messages.
func (c *conn) reply(tag uint32) (*reader, error) {
	for {
		p, _, err := c.pr.read()
		if err != nil {
			return nil, err
		}
		if p.channel != controlChannel {
			continue
		}
		r := &reader{b: p.data}
		cmd, t := r.u32(), r.u32()
		switch {
		case r.err != nil:
			return nil, r.err
		case t != tag:
			continue
		case cmd == cmdError:
			return nil, fmt.Errorf("error %d", r.u32())
		case cmd != cmdReply:
			return nil, fmt.Errorf("command %d in reply", cmd)
		}
		return r, nil
	}
}

// Probe connects to the server and authenticates, as each of a meter's
// streams does, and hangs up.
func Probe(ctx context.Context) error {
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	return c.c.Close()
}

// dial connects to the server and authenticates.
func dial(ctx context.Context) (*conn, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: ioTimeout}
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	c := &conn{c: nc, pr: newPacketReader(nc)}
	nc.SetDeadline(time.Now().Add(ioTimeout))
	if err := c.handshake(); err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// handshake authenticates and names the client, and checks the server
// speaks a version this client knows.
func (c *conn) handshake() error {
	auth := c.command(cmdAuth)
	auth.u32(protocolVersion)
	auth.arbitrary(cookie())
	if err := writePacket(c.c, auth.b); err != nil {
		return err
	}
	r, err := c.reply(c.tag)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if v := r.u32() & 0xffff; r.err == nil && v < minVersion {
		return fmt.Errorf("server speaks protocol version %d; need %d", v, minVersion)
	}
	name := c.command(cmdSetClientName)
	name.proplist([][2]string{
		{"application.name", "pipmix"},
		{"application.id", "pipmix"},
		{"application.process.binary", "pipmix"},
		{"application.process.id", fmt.Sprint(os.Getpid())},
	})
	if err := writePacket(c.c, name.b); err != nil {
		return err
	}
	if _, err := c.reply(c.tag); err != nil {
		return fmt.Errorf("set client name: %w", err)
	}
	return nil
}

// create opens a peak-detect record stream for t and returns its index and
// channel count.
func (c *conn) create(t Target, rate int, nodeName string) (uint32, int, error) {
	positions := []byte{0}
	if len(t.Channels) > 0 {
		positions = positions[:0]
		for i, name := range t.Channels {
			positions = append(positions, position(name, i))
		}
	}
	n := byte(len(positions))
	b := c.command(cmdCreateRecordStream)
	b.sampleSpec(formatFloat32LE, n, uint32(rate))
	b.channelMap(positions)
	b.u32(invalidIndex)
	switch t.Kind {
	case Sink:
		b.string(t.Name + ".monitor")
	case Source:
		b.string(t.Name)
	case JACK:
		b.string("@DEFAULT_MONITOR@")
	default:
		b.null()
	}
	b.u32(invalidIndex)  // maxlength
	b.bool(false)        // corked
	b.u32(4 * uint32(n)) // fragsize
	// no_remap, no_remix, fix_format, fix_rate, fix_channels, no_move,
	// variable_rate
	for _, v := range []bool{false, false, false, false, false, true, false} {
		b.bool(v)
	}
	b.bool(true) // peak_detect
	b.bool(true) // adjust_latency
	props := [][2]string{{"media.name", "Peak meter"}}
	if nodeName != "" {
		props = append(props, [2]string{"node.name", nodeName})
	}
	b.proplist(props)
	direct := uint32(invalidIndex)
	if t.Kind == Stream {
		direct = t.Serial
	}
	b.u32(direct)
	b.bool(false) // early_requests
	b.bool(true)  // dont_inhibit_auto_suspend
	b.bool(false) // fail_on_suspend
	b.u8(0)       // no format infos
	volumes := make([]uint32, n)
	for i := range volumes {
		volumes[i] = 0x10000 // PA_VOLUME_NORM
	}
	b.cvolume(volumes)
	// muted, volume_set, muted_set, relative_volume, passthrough
	b.bool(false) // muted
	b.bool(true)  // volume_set
	b.bool(true)  // muted_set
	b.bool(false) // relative_volume
	b.bool(false) // passthrough
	if err := writePacket(c.c, b.b); err != nil {
		return 0, 0, err
	}
	r, err := c.reply(c.tag)
	if err != nil {
		return 0, 0, fmt.Errorf("create record stream: %w", err)
	}
	r.u32() // channel
	index := r.u32()
	r.u32() // maxlength
	r.u32() // fragsize
	format, channels, _ := r.sampleSpec()
	switch {
	case r.err != nil:
		return 0, 0, fmt.Errorf("create record stream: %w", r.err)
	case format != formatFloat32LE || channels == 0:
		return 0, 0, fmt.Errorf("record stream has format %d, %d channels", format, channels)
	}
	return index, int(channels), nil
}
