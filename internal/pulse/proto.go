package pulse

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// The native protocol, as pipewire-pulse speaks it
// (src/modules/module-protocol-pulse in the PipeWire source).
const (
	protocolVersion = 35
	cookieLength    = 256
	controlChannel  = math.MaxUint32
	invalidIndex    = math.MaxUint32
	descriptorSize  = 20
	// maxPacket bounds a packet's payload: a record stream at a peak
	// detector's rate sends a frame at a time.
	maxPacket = 1 << 20
)

// Commands, the protocol's enum pulseaudio_command.
const (
	cmdError              = 0
	cmdReply              = 2
	cmdCreateRecordStream = 5
	cmdDeleteRecordStream = 6
	cmdAuth               = 8
	cmdSetClientName      = 9
	cmdRecordStreamKilled = 65
)

// Tag types of a tagstruct.
const (
	tagString     = 't'
	tagStringNull = 'N'
	tagU32        = 'L'
	tagU8         = 'B'
	tagU64        = 'R'
	tagS64        = 'r'
	tagSampleSpec = 'a'
	tagArbitrary  = 'x'
	tagTrue       = '1'
	tagFalse      = '0'
	tagTimeval    = 'T'
	tagUsec       = 'U'
	tagChannelMap = 'm'
	tagCVolume    = 'v'
	tagProplist   = 'P'
	tagVolume     = 'V'
	tagFormatInfo = 'f'
)

const formatFloat32LE = 5

// channelPositions are pa_channel_position_t, by PipeWire's short names.
var channelPositions = map[string]byte{
	"MONO": 0, "FL": 1, "FR": 2, "FC": 3, "RL": 4, "RR": 5, "RC": 6, "LFE": 7,
	"FLC": 8, "FRC": 9, "SL": 10, "SR": 11, "TC": 44, "TFL": 45, "TFR": 46,
	"TFC": 47, "TRL": 48, "TRR": 49, "TRC": 50,
}

// position returns the channel position for a PipeWire channel name, an
// AUXn for one Pulse has no name for.
func position(name string, i int) byte {
	if p, ok := channelPositions[name]; ok {
		return p
	}
	var n int
	if _, err := fmt.Sscanf(name, "AUX%d", &n); err == nil && n >= 0 && n < 32 {
		return byte(12 + n)
	}
	return byte(12 + i%32)
}

// SocketPath returns the server's socket: $PULSE_SERVER if it's a local
// one, else native in $PULSE_RUNTIME_PATH or $XDG_RUNTIME_DIR/pulse.
func SocketPath() (string, error) {
	if s := os.Getenv("PULSE_SERVER"); s != "" {
		for addr := range strings.FieldsSeq(s) {
			if path, ok := strings.CutPrefix(addr, "unix:"); ok {
				return path, nil
			}
		}
		return "", fmt.Errorf("PULSE_SERVER %q has no local socket", s)
	}
	if dir := os.Getenv("PULSE_RUNTIME_PATH"); dir != "" {
		return filepath.Join(dir, "native"), nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "pulse", "native"), nil
	}
	return "", errors.New("neither PULSE_RUNTIME_PATH nor XDG_RUNTIME_DIR is set")
}

// cookie returns the PulseAudio authentication cookie.
func cookie() []byte {
	paths := []string{os.Getenv("PULSE_COOKIE")}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "pulse", "cookie"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".pulse-cookie"))
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && len(b) == cookieLength {
			return b
		}
	}
	return make([]byte, cookieLength)
}

// A packet is a message on a channel: a command on the control channel,
// else a stream's audio.
type packet struct {
	channel uint32
	data    []byte
}

// A packetReader reads packets through a buffered reader, reusing its
// header and payload buffers across calls.
type packetReader struct {
	r   *bufio.Reader
	hdr [descriptorSize]byte
	buf []byte
}

func newPacketReader(r io.Reader) *packetReader {
	return &packetReader{r: bufio.NewReaderSize(r, 4096)}
}

// read reads the next packet. The returned data slice is overwritten by
// the next call to read. idleTimeout is true if a read timeout occurred
// before any byte of the packet was read.
func (pr *packetReader) read() (p packet, idleTimeout bool, err error) {
	if _, err := pr.r.Peek(1); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return packet{}, true, err
		}
		return packet{}, false, err
	}
	if _, err := io.ReadFull(pr.r, pr.hdr[:]); err != nil {
		return packet{}, false, err
	}
	n := binary.BigEndian.Uint32(pr.hdr[0:])
	if n > maxPacket {
		return packet{}, false, fmt.Errorf("packet of %d bytes", n)
	}
	if cap(pr.buf) < int(n) {
		pr.buf = make([]byte, n)
	}
	data := pr.buf[:n]
	if _, err := io.ReadFull(pr.r, data); err != nil {
		return packet{}, false, err
	}
	return packet{channel: binary.BigEndian.Uint32(pr.hdr[4:]), data: data}, false, nil
}

// writePacket writes a control packet.
func writePacket(c net.Conn, data []byte) error {
	b := make([]byte, descriptorSize, descriptorSize+len(data))
	binary.BigEndian.PutUint32(b[0:], uint32(len(data)))
	binary.BigEndian.PutUint32(b[4:], controlChannel)
	_, err := c.Write(append(b, data...))
	return err
}

// A builder writes a tagstruct.
type builder struct{ b []byte }

func command(cmd, tag uint32) *builder {
	b := &builder{}
	b.u32(cmd)
	b.u32(tag)
	return b
}

func (b *builder) u32(v uint32) {
	b.b = append(b.b, tagU32)
	b.b = binary.BigEndian.AppendUint32(b.b, v)
}

func (b *builder) u8(v byte) { b.b = append(b.b, tagU8, v) }

func (b *builder) bool(v bool) {
	if v {
		b.b = append(b.b, tagTrue)
	} else {
		b.b = append(b.b, tagFalse)
	}
}

func (b *builder) string(s string) {
	b.b = append(b.b, tagString)
	b.b = append(b.b, s...)
	b.b = append(b.b, 0)
}

func (b *builder) null() { b.b = append(b.b, tagStringNull) }

func (b *builder) arbitrary(p []byte) {
	b.b = append(b.b, tagArbitrary)
	b.b = binary.BigEndian.AppendUint32(b.b, uint32(len(p)))
	b.b = append(b.b, p...)
}

func (b *builder) sampleSpec(format, channels byte, rate uint32) {
	b.b = append(b.b, tagSampleSpec, format, channels)
	b.b = binary.BigEndian.AppendUint32(b.b, rate)
}

func (b *builder) channelMap(positions []byte) {
	b.b = append(b.b, tagChannelMap, byte(len(positions)))
	b.b = append(b.b, positions...)
}

func (b *builder) cvolume(volumes []uint32) {
	b.b = append(b.b, tagCVolume, byte(len(volumes)))
	for _, v := range volumes {
		b.b = binary.BigEndian.AppendUint32(b.b, v)
	}
}

// proplist writes props; values are strings, sent with their NUL.
func (b *builder) proplist(props [][2]string) {
	b.b = append(b.b, tagProplist)
	for _, kv := range props {
		b.string(kv[0])
		v := append([]byte(kv[1]), 0)
		b.u32(uint32(len(v)))
		b.arbitrary(v)
	}
	b.null()
}

// A reader reads a tagstruct.
type reader struct {
	b   []byte
	err error
}

var errShort = errors.New("short tagstruct")

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n > len(r.b) {
		r.fail(errShort)
		return nil
	}
	p := r.b[:n]
	r.b = r.b[n:]
	return p
}

// tag reads a value's tag and checks that it's want.
func (r *reader) tag(want ...byte) byte {
	p := r.take(1)
	if p == nil {
		return 0
	}
	for _, w := range want {
		if p[0] == w {
			return w
		}
	}
	r.fail(fmt.Errorf("tag %q, want %q", p[0], want))
	return 0
}

func (r *reader) u32() uint32 {
	r.tag(tagU32)
	if p := r.take(4); p != nil {
		return binary.BigEndian.Uint32(p)
	}
	return 0
}

func (r *reader) sampleSpec() (format, channels byte, rate uint32) {
	r.tag(tagSampleSpec)
	if p := r.take(6); p != nil {
		return p[0], p[1], binary.BigEndian.Uint32(p[2:])
	}
	return 0, 0, 0
}
