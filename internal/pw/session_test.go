package pw

import (
	"net"
	"path/filepath"
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
