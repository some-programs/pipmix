package pw

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testConf = `
context.properties = { core.daemon = true core.name = pipewire-0 support.dbus = false log.level = 2 }
context.spa-libs = {
    audio.convert.* = audioconvert/libspa-audioconvert
    support.*       = support/libspa-support
}
context.modules = [
    { name = libpipewire-module-protocol-native }
    { name = libpipewire-module-access }
    { name = libpipewire-module-metadata }
    { name = libpipewire-module-adapter }
    { name = libpipewire-module-spa-node-factory }
]
context.objects = [
    { factory = metadata args = { metadata.name = default } }
    { factory = adapter args = { factory.name = support.null-audio-sink node.name = test-sink
        node.description = "Test Sink" media.class = Audio/Sink audio.position = [ FL FR ] } }
    { factory = adapter args = { factory.name = support.null-audio-sink node.name = test-source
        node.description = "Test Source" media.class = Audio/Source audio.position = [ MONO ] } }
    { factory = adapter args = { factory.name = support.null-audio-sink node.name = test-stream
        node.description = "Test Player" media.name = "Some Song"
        media.class = Stream/Output/Audio audio.position = [ FL FR ] } }
    { factory = spa-node-factory args = { factory.name = support.null-audio-sink node.name = test-jack
        node.description = "Test JACK" client.api = jack media.type = Audio audio.position = [ MONO ] } }
    { factory = adapter args = { factory.name = support.null-audio-sink node.name = pipmix.meter.%d.sink.1
        node.description = "Test Meter Sink" media.class = Stream/Input/Audio audio.position = [ FL FR ] } }
]
`

// startPipeWire runs a private PipeWire for the test and points the
// client at it.
func startPipeWire(t *testing.T) (stop func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a PipeWire daemon")
	}
	bin, err := exec.LookPath("pipewire")
	if err != nil {
		t.Skip("pipewire not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "test.conf")
	if err := os.WriteFile(conf, []byte(fmt.Sprintf(testConf, os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPEWIRE_RUNTIME_DIR", dir)
	t.Setenv("PIPEWIRE_REMOTE", "")
	cmd := exec.Command(bin, "-c", conf)
	cmd.Env = append(os.Environ(), "PIPEWIRE_RUNTIME_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop = func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}
	t.Cleanup(stop)
	for range 100 {
		if _, err := os.Stat(filepath.Join(dir, "pipewire-0")); err == nil {
			return stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pipewire didn't create its socket")
	return nil
}

// waitFor waits until cond holds for the client's snapshot.
func waitFor(t *testing.T, c *Client, what string, cond func(*Snapshot) bool) *Snapshot {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if s := c.Snapshot(); cond(s) {
			return s
		}
		select {
		case <-c.Updated():
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last snapshot: %+v", what, c.Snapshot())
		}
	}
}

func findNode(s *Snapshot, name string) *Node {
	for i := range s.Nodes {
		if s.Nodes[i].Name == name {
			return &s.Nodes[i]
		}
	}
	return nil
}

func TestLive(t *testing.T) {
	startPipeWire(t)
	c := NewClient(log.New(testWriter{t}, "", 0))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go c.Run(ctx)

	has := func(name string) func(*Snapshot) bool {
		return func(s *Snapshot) bool {
			n := findNode(s, name)
			return n != nil && len(n.Channels) > 0
		}
	}
	s := waitFor(t, c, "nodes with volumes", func(s *Snapshot) bool {
		return has("test-sink")(s) && has("test-source")(s) && has("test-stream")(s)
	})
	sink := findNode(s, "test-sink")
	if sink.Class != Sink || len(sink.Channels) != 2 || sink.Channels[0].Name != "FL" || sink.Channels[1].Name != "FR" {
		t.Errorf("sink = %+v", sink)
	}
	if src := findNode(s, "test-source"); src.Class != Source || len(src.Channels) != 1 || src.Channels[0].Name != "MONO" {
		t.Errorf("source = %+v", src)
	}
	if st := findNode(s, "test-stream"); st.Class != Playback || st.Title != "Test Player: Some Song" {
		t.Errorf("stream = %+v", st)
	}
	s = waitFor(t, c, "the JACK client", func(s *Snapshot) bool { return findNode(s, "test-jack") != nil })
	var jack []Node
	for _, n := range s.Nodes {
		if n.Name == "test-jack" {
			jack = append(jack, n)
		}
	}
	if len(jack) != 1 || jack[0].Class != Recording || !jack[0].JACK || len(jack[0].Channels) != 0 {
		t.Errorf("JACK client = %+v", jack)
	}

	// Volume: steps apply on top of each other at once, and the server's
	// value agrees once the pending window is over.
	id := sink.ID
	start := sink.Channels[0].Volume
	c.ChangeVolume(id, -1, -0.10, 1.5)
	c.ChangeVolume(id, -1, -0.10, 1.5)
	c.ChangeVolume(id, 1, -0.05, 1.5)
	want := []float64{start - 0.20, start - 0.25}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.005 }
	check := func(s *Snapshot) bool {
		n := findNode(s, "test-sink")
		return n != nil && len(n.Channels) == 2 && near(n.Channels[0].Volume, want[0]) && near(n.Channels[1].Volume, want[1])
	}
	waitFor(t, c, "volume change", check)
	time.Sleep(pendingWindow + 100*time.Millisecond)
	waitFor(t, c, "volume confirmed by the server", check)

	// The limit holds.
	for range 30 {
		c.ChangeVolume(id, -1, 0.05, 1.2)
	}
	waitFor(t, c, "volume at the limit", func(s *Snapshot) bool {
		n := findNode(s, "test-sink")
		return n != nil && near(n.Channels[0].Volume, 1.2) && near(n.Channels[1].Volume, 1.2)
	})

	c.ToggleMute(id)
	time.Sleep(pendingWindow + 100*time.Millisecond)
	waitFor(t, c, "mute", func(s *Snapshot) bool {
		n := findNode(s, "test-sink")
		return n != nil && n.Mute
	})

	c.SetDefault(id)
	waitFor(t, c, "default", func(s *Snapshot) bool {
		n := findNode(s, "test-sink")
		return n != nil && n.Default
	})

	meterNodeName := fmt.Sprintf("pipmix.meter.%d.sink.1", os.Getpid())
	if findNode(s, meterNodeName) != nil {
		t.Errorf("meter node should not be in snapshot")
	}
	var meterGlobalID uint32
	meterChecked := make(chan bool, 1)
	c.do(func(sess *session) {
		for id := range sess.meters {
			meterGlobalID = id
		}
		meterChecked <- len(sess.meters) == 1
	})
	select {
	case ok := <-meterChecked:
		if !ok {
			t.Errorf("expected 1 meter node in session")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for session command")
	}

	// External tool alters meter volume / mute; pipmix resets them to 100% unmuted.
	cmd := exec.Command("pw-cli", "s", strconv.FormatUint(uint64(meterGlobalID), 10), "Props", `{ "channelVolumes": [ 0.3, 0.3 ], "mute": true }`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pw-cli set-param failed: %v: %s", err, out)
	}

	// Verify the meter node is reset to volume 1.0 and unmuted.
	deadline := time.After(3 * time.Second)
	for {
		out, err := exec.Command("pw-cli", "e", strconv.FormatUint(uint64(meterGlobalID), 10), "Props").CombinedOutput()
		sout := string(out)
		if err == nil && strings.Contains(sout, "Bool false") && (strings.Contains(sout, "1.000000") || strings.Contains(sout, "1,000000")) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for meter node volume reset; last props: %s", out)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// A client from another instance (with another PID) hides foreign meter
	// streams from its snapshot, and does not track or enforce volume on them.
	cOther := NewClient(log.New(testWriter{t}, "other: ", 0))
	cOther.PID = 99999
	go cOther.Run(ctx)
	sOther := waitFor(t, cOther, "other client", func(s *Snapshot) bool { return findNode(s, "test-sink") != nil })
	if findNode(sOther, meterNodeName) != nil {
		t.Errorf("other instance should hide foreign meter node from snapshot")
	}
	cOther.do(func(sess *session) {
		if len(sess.meters) != 0 {
			t.Errorf("other instance should not track foreign meter node, got %d", len(sess.meters))
		}
	})
}

// TestReconnect restarts PipeWire under a connected client.
func TestReconnect(t *testing.T) {
	stop := startPipeWire(t)
	c := NewClient(log.New(testWriter{t}, "", 0))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, c, "connection", func(s *Snapshot) bool { return findNode(s, "test-sink") != nil })

	stop()
	waitFor(t, c, "disconnection", func(s *Snapshot) bool { return !s.Connected && s.Err != nil && len(s.Nodes) == 0 })

	startPipeWire(t)
	waitFor(t, c, "reconnection", func(s *Snapshot) bool {
		n := findNode(s, "test-sink")
		return s.Connected && n != nil && len(n.Channels) == 2
	})
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
