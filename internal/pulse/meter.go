// Package pulse measures audio peak and average levels via pipewire-pulse.
package pulse

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
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
)

// A Target is a node to measure.
type Target struct {
	Kind     Kind
	Name     string // node.name of a Sink or Source
	Serial   uint32 // object.serial of a Stream
	Channels []string
}

func (t Target) String() string {
	switch t.Kind {
	case Sink:
		return "sink " + t.Name
	case Source:
		return "source " + t.Name
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

// A Meter measures the levels of a set of targets.
type Meter struct {
	log     *log.Logger
	rate    int
	want    chan map[uint32]Target
	snap    atomic.Pointer[Snapshot]
	updated chan struct{}
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
	m := &Meter{
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

// A level is a worker's latest peaks and averages; nil when it has none.
type level struct {
	w           *worker
	key         uint32
	peaks, avgs []float64
}

// Run measures until ctx is done.
func (m *Meter) Run(ctx context.Context) {
	workers := make(map[uint32]*worker)
	// latest are the targets' latest peaks; held the highest since the
	// last snapshot, which starts from latest.
	latest := make(map[uint32][]float64)
	held := make(map[uint32][]float64)
	avgs := make(map[uint32][]float64)
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
					if _, ok := latest[key]; ok {
						delete(latest, key)
						delete(held, key)
						delete(avgs, key)
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
			if l.peaks == nil {
				delete(latest, l.key)
				delete(held, l.key)
				delete(avgs, l.key)
			} else {
				latest[l.key] = l.peaks
				held[l.key] = highest(held[l.key], l.peaks)
				avgs[l.key] = l.avgs
			}
			dirty = true
		case <-flush.C:
			flushing = false
			published = time.Now()
			m.snap.Store(&Snapshot{Peaks: held, Averages: maps.Clone(avgs)})
			// A level that fell while held shows in the next snapshot.
			dirty = !maps.EqualFunc(held, latest, slices.Equal)
			held = maps.Clone(latest)
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

// highest returns each channel's highest of a and b, in a new slice; b if
// they don't match.
func highest(a, b []float64) []float64 {
	if len(a) != len(b) {
		return b
	}
	h := make([]float64, len(a))
	for i := range a {
		h[i] = max(a[i], b[i])
	}
	return h
}

// measure runs w's streams until ctx is done, waiting longer after each
// that fails before it makes another, and sends their peaks to updates.
func (m *Meter) measure(ctx context.Context, w *worker, key uint32, updates chan<- level) {
	send := func(peaks, avgs []float64) bool {
		select {
		case updates <- level{w, key, peaks, avgs}:
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

	nc.SetReadDeadline(time.Now().Add(createWait))
	m.log.Printf("pulse: %v: creating a record stream; the reply waits for the node to run", t)
	asked := time.Now()
	index, channels, err := c.create(t, m.rate)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false, errIdle
	}
	if err != nil {
		return false, err
	}
	nc.SetDeadline(time.Time{})
	m.log.Printf("pulse: %v: record stream %d created after %v", t, index, time.Since(asked).Round(time.Millisecond))

	frame := 4 * channels
	ms := make([]float64, channels)
	alpha := 1 - math.Exp(-1/(float64(m.rate)*averageTime.Seconds()))
	var last, lastAvgs []float64
	silent, since := true, time.Now()
	for {
		p, err := readPacket(nc)
		if errors.As(err, &ne) && ne.Timeout() {
			nc.SetReadDeadline(time.Time{})
			silent, since = true, time.Now()
			m.log.Printf("pulse: %v: no peaks for %v; showing 0 until they come", t, max(stale, 3*m.Interval()))
			clear(ms)
			last, lastAvgs = make([]float64, channels), make([]float64, channels)
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
		// Usually one frame; if more came, each channel's largest.
		peaks := make([]float64, channels)
		for f := 0; f+frame <= len(p.data); f += frame {
			for ch := range channels {
				v := math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(p.data[f+4*ch:]))))
				peaks[ch] = max(peaks[ch], v)
				ms[ch] += alpha * (v*v - ms[ch])
			}
		}
		avgs := make([]float64, channels)
		for ch := range avgs {
			avgs[ch] = math.Sqrt(ms[ch])
		}
		if silent {
			m.log.Printf("pulse: %v: peaks after %v", t, time.Since(since).Round(time.Millisecond))
			silent = false
		}
		ran = true
		nc.SetReadDeadline(time.Now().Add(max(stale, 3*m.Interval())))
		if !slices.Equal(peaks, last) || !slices.Equal(avgs, lastAvgs) {
			last, lastAvgs = peaks, avgs
			if !send(peaks, avgs) {
				return ran, ctx.Err()
			}
		}
	}
}

// A conn is a connection to the server, while it's set up.
type conn struct {
	c   net.Conn
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
		p, err := readPacket(c.c)
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
	c := &conn{c: nc}
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
func (c *conn) create(t Target, rate int) (uint32, int, error) {
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
	b.proplist([][2]string{{"media.name", "Peak meter"}})
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
	for range 5 {
		b.bool(false)
	}
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
