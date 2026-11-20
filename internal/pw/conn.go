package pw

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	writeTimeout = 2 * time.Second
	maxMessage   = 1 << 24
)

// SocketPath returns the path of the PipeWire socket.
func SocketPath() (string, error) {
	name := cmp.Or(os.Getenv("PIPEWIRE_REMOTE"), "pipewire-0")
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir := cmp.Or(os.Getenv("PIPEWIRE_RUNTIME_DIR"), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("USERPROFILE"))
	if dir == "" {
		return "", errors.New("neither PIPEWIRE_RUNTIME_DIR nor XDG_RUNTIME_DIR is set")
	}
	return filepath.Join(dir, name), nil
}

type message struct {
	id      uint32
	opcode  uint8
	payload pod
	footer  pod
}

type conn struct {
	c    *net.UnixConn
	buf  []byte // backing buffer for in
	in   []byte // unread bytes, subslice of buf
	oob  []byte
	seq  uint32
	wbuf []byte
}

func dial() (*conn, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return &conn{c: c, oob: make([]byte, syscall.CmsgSpace(28*4))}, nil
}

func (c *conn) close() error { return c.c.Close() }

// read returns the next message.
func (c *conn) read() (message, error) {
	for {
		if m, n, ok, err := parseMessage(c.in); err != nil {
			return message{}, err
		} else if ok {
			// Copy, so the message doesn't pin or share the read buffer.
			m.payload.body = append([]byte(nil), m.payload.body...)
			m.footer.body = append([]byte(nil), m.footer.body...)
			c.in = c.in[n:]
			return m, nil
		}
		if err := c.fill(); err != nil {
			return message{}, err
		}
	}
}

// fill reads more data into c.in.
func (c *conn) fill() error {
	if len(c.in) == 0 {
		c.in = c.buf[:0]
	} else if cap(c.in)-len(c.in) < 4096 && len(c.buf)-len(c.in) >= 4096 {
		copy(c.buf, c.in)
		c.in = c.buf[:len(c.in)]
	}
	if cap(c.in)-len(c.in) < 4096 {
		buf := make([]byte, 2*len(c.buf)+65536)
		copy(buf, c.in)
		c.buf = buf
		c.in = buf[:len(c.in)]
	}
	n, oobn, _, _, err := c.c.ReadMsgUnix(c.in[len(c.in):cap(c.in)], c.oob)
	if oobn > 0 {
		closeFds(c.oob[:oobn])
	}
	if n == 0 && err == nil {
		err = io.EOF
	}
	if err != nil {
		return err
	}
	c.in = c.in[:len(c.in)+n]
	return nil
}

// closeFds closes any file descriptors received in oob.
func closeFds(oob []byte) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			syscall.Close(fd)
		}
	}
}

// parseMessage parses the message at the start of b. ok is false if b
// doesn't hold all of it yet; n is its length.
func parseMessage(b []byte) (m message, n int, ok bool, err error) {
	if len(b) < 16 {
		return m, 0, false, nil
	}
	m.id = native.Uint32(b)
	w := native.Uint32(b[4:])
	m.opcode = uint8(w >> 24)
	size := int(w & 0xffffff)
	if len(b) < 16+size {
		return m, 0, false, nil
	}
	body := b[16 : 16+size]
	m.payload, body, err = readPod(body)
	if err != nil {
		return m, 0, false, fmt.Errorf("message %d/%d: %w", m.id, m.opcode, err)
	}
	m.footer = pod{typ: typeNone}
	if len(body) >= 8 {
		if f, _, err := readPod(body); err == nil {
			m.footer = f
		}
	}
	return m, 16 + size, true, nil
}

// write sends a message to proxy id. payload and footer are encoded PODs;
// footer may be empty.
func (c *conn) write(id uint32, opcode uint8, payload, footer []byte) error {
	size := len(payload) + len(footer)
	if size >= maxMessage {
		return fmt.Errorf("message %d/%d too large: %d bytes", id, opcode, size)
	}
	c.seq++
	b := c.wbuf[:0]
	b = native.AppendUint32(b, id)
	b = native.AppendUint32(b, uint32(opcode)<<24|uint32(size))
	b = native.AppendUint32(b, c.seq)
	b = native.AppendUint32(b, 0)
	b = append(b, payload...)
	b = append(b, footer...)
	c.wbuf = b
	if err := c.c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	_, err := c.c.Write(b)
	return err
}
