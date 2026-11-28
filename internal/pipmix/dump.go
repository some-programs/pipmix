package pipmix

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/some-programs/pipmix/internal/mpris"
	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

var dumpEnv = []string{
	"PIPEWIRE_RUNTIME_DIR", "PIPEWIRE_REMOTE", "XDG_RUNTIME_DIR",
	"PULSE_SERVER", "PULSE_RUNTIME_PATH", "DBUS_SESSION_BUS_ADDRESS",
	"TERM", "COLORTERM", "TERM_PROGRAM", "TMUX",
}

// dump writes diagnostic information for -debug.dump.
func dump(ctx context.Context, w io.Writer, logs *logBuffer, flags *flag.FlagSet, opts options, c *pw.Client, media *mpris.Watcher, meter *pulse.Meter) error {
	fmt.Fprintln(w, "[build]")
	dumpBuild(w)

	fmt.Fprintln(w, "\n[config]")
	cfg := flags.Lookup("config").Value.String()
	if _, err := os.Stat(cfg); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "file %s (missing)\n", cfg)
	} else if err != nil {
		fmt.Fprintf(w, "file %s: %v\n", cfg, err)
	} else {
		fmt.Fprintf(w, "file %s\n", cfg)
	}
	flags.Visit(func(f *flag.Flag) {
		fmt.Fprintf(w, "flag -%s=%s\n", f.Name, f.Value)
	})

	fmt.Fprintln(w, "\n[environment]")
	for _, k := range dumpEnv {
		if v, ok := os.LookupEnv(k); ok {
			fmt.Fprintf(w, "%s=%s\n", k, v)
		}
	}

	var failed []string
	fmt.Fprintln(w, "\n[pipewire]")
	s := waitPipeWire(c)
	if s.Connected {
		path, _ := pw.SocketPath()
		fmt.Fprintf(w, "connected to %s\n", path)
	} else {
		fmt.Fprintf(w, "not connected: %v\n", orTimeout(s.Err))
		failed = append(failed, "pipewire")
	}
	players := &mpris.Snapshot{}
	if media != nil {
		players = waitMPRIS(media)
	}
	m := newMixer(c, media, nil, section{}, opts)
	m.snap = s
	m.findMedia()
	shown := make(map[uint32]bool)
	for _, it := range m.items(tabPlayback) {
		shown[it.id] = true
	}
	dumpNodes(w, s, shown, opts)

	fmt.Fprintln(w, "\n[mpris]")
	switch {
	case media == nil:
		fmt.Fprintln(w, "off")
	case players.Connected:
		addr, _ := mpris.SessionBus()
		fmt.Fprintf(w, "connected to %s\n", addr)
	default:
		fmt.Fprintf(w, "not connected: %v\n", orTimeout(players.Err))
		failed = append(failed, "mpris")
	}
	for _, p := range players.Players {
		dumpPlayer(w, p)
	}
	for _, n := range s.Nodes {
		if p, ok := m.mediaPlayers[n.ID]; ok {
			fmt.Fprintf(w, "node %d %q plays from %s\n", n.ID, n.Title, p.Name)
		}
	}

	fmt.Fprintln(w, "\n[pulse]")
	targets := make(map[uint32]pulse.Target)
	for i := range s.Nodes {
		if t, ok := meterTarget(s, &s.Nodes[i]); ok {
			targets[s.Nodes[i].ID] = t
		}
	}
	if meter == nil {
		fmt.Fprintln(w, "off")
	} else if err := probePulse(ctx); err != nil {
		fmt.Fprintf(w, "not connected: %v\n", err)
		failed = append(failed, "pulse")
	} else {
		path, _ := pulse.SocketPath()
		fmt.Fprintf(w, "connected to %s\n", path)
		dumpLevels(w, s, targets, measure(meter, targets))
	}

	fmt.Fprintln(w, "\n[log]")
	fmt.Fprint(w, logs.String())

	if failed != nil {
		return fmt.Errorf("%s not connected", strings.Join(failed, ", "))
	}
	return nil
}

func dumpNodes(w io.Writer, s *pw.Snapshot, shown map[uint32]bool, opts options) {
	classes := []string{"playback", "recording", "sink", "source"}
	for _, n := range s.Nodes {
		fmt.Fprintf(w, "node %d %s %q (%s) %s", n.ID, classes[n.Class], n.Title, n.Name, n.State)
		if n.Active {
			fmt.Fprint(w, " active")
		}
		if n.Default {
			fmt.Fprint(w, " default")
		}
		if n.JACK {
			fmt.Fprint(w, " jack")
		}
		if n.Mute {
			fmt.Fprint(w, " muted")
		}
		fmt.Fprintln(w)
		var props []string
		for _, k := range []string{"object.serial", "application.process.id", "application.process.binary"} {
			if v := n.Props[k]; v != "" {
				props = append(props, k+"="+v)
			}
		}
		if len(props) > 0 {
			fmt.Fprintf(w, "  %s\n", strings.Join(props, " "))
		}
		switch {
		case n.Class != pw.Playback || shown[n.ID]:
		case n.JACK && !opts.jack:
			fmt.Fprintln(w, "  hidden: -jack.enable=false")
		default:
			fmt.Fprintln(w, "  hidden: duplicates a multi-channel stream")
		}
		for _, ch := range n.Channels {
			if n.JACK {
				fmt.Fprintf(w, "    %s\n", ch.Name)
			} else {
				fmt.Fprintf(w, "    %-4s %3.0f%%\n", ch.Name, ch.Volume*100)
			}
		}
		for i, r := range n.Routes {
			mark := " "
			if i == n.Route {
				mark = "*"
			}
			fmt.Fprintf(w, "  %s route %d %s (%s) available=%v\n", mark, r.Index, r.Description, r.Name, r.Available)
		}
	}
	for _, c := range s.Cards {
		fmt.Fprintf(w, "card %d %q\n", c.ID, c.Title)
		for i, p := range c.Profiles {
			mark := " "
			if i == c.Profile {
				mark = "*"
			}
			fmt.Fprintf(w, "  %s profile %d %s (%s) available=%v\n", mark, p.Index, p.Description, p.Name, p.Available)
		}
	}
}

func dumpLevels(w io.Writer, s *pw.Snapshot, targets map[uint32]pulse.Target, levels *pulse.Snapshot) {
	for _, n := range s.Nodes {
		t, ok := targets[n.ID]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "node %d %v\n", n.ID, t)
		peaks, avgs := levels.Peaks[n.ID], levels.Averages[n.ID]
		if peaks == nil {
			fmt.Fprintf(w, "  no levels in %v: the node may not be running\n", measureTime)
			continue
		}
		for i, ch := range t.Channels {
			if i < len(peaks) && i < len(avgs) {
				fmt.Fprintf(w, "    %-4s peak %s  average %s\n", ch, decibels(peaks[i]), decibels(avgs[i]))
			}
		}
	}
}

func dumpBuild(w io.Writer) {
	fmt.Fprint(w, "pipmix")
	if bi, ok := debug.ReadBuildInfo(); ok {
		fmt.Fprintf(w, " %s", bi.Main.Version)
		for _, s := range bi.Settings {
			if strings.HasPrefix(s.Key, "vcs.") {
				fmt.Fprintf(w, " %s=%s", s.Key, s.Value)
			}
		}
	}
	fmt.Fprintf(w, " %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func dumpPlayer(w io.Writer, p mpris.Player) {
	state := "paused"
	if p.Playing {
		state = "playing"
	}
	fmt.Fprintf(w, "player %s pid %d %s %q\n", p.Name, p.PID, state, p.Title)
}

const (
	connectTime = 5 * time.Second
	measureTime = time.Second
)

func orTimeout(err error) error {
	if err == nil {
		return fmt.Errorf("no answer in %v", connectTime)
	}
	return err
}

// waitPipeWire waits for c's initial updates to settle, up to connectTime.
func waitPipeWire(c *pw.Client) *pw.Snapshot {
	deadline := time.After(connectTime)
	for {
		select {
		case <-c.Updated():
			continue
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
		}
		break
	}
	return c.Snapshot()
}

func waitMPRIS(media *mpris.Watcher) *mpris.Snapshot {
	deadline := time.After(connectTime)
	for {
		if s := media.Snapshot(); s.Connected || s.Err != nil {
			return s
		}
		select {
		case <-media.Updated():
		case <-deadline:
			return media.Snapshot()
		}
	}
}

func probePulse(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, connectTime)
	defer cancel()
	return pulse.Probe(ctx)
}

// measure records targets for measureTime and returns the highest peaks and
// latest averages.
func measure(meter *pulse.Meter, targets map[uint32]pulse.Target) *pulse.Snapshot {
	meter.Watch(targets)
	defer meter.Watch(nil)
	levels := &pulse.Snapshot{Peaks: make(map[uint32][]float64), Averages: make(map[uint32][]float64)}
	deadline := time.After(measureTime)
	for {
		select {
		case <-meter.Updated():
		case <-deadline:
			return levels
		}
		s := meter.Snapshot()
		for k, peaks := range s.Peaks {
			levels.Peaks[k] = highest(levels.Peaks[k], peaks)
		}
		maps.Copy(levels.Averages, s.Averages)
	}
}

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

func decibels(v float64) string {
	if v <= 0 {
		return "-inf dB"
	}
	return fmt.Sprintf("%.1f dB", 20*math.Log10(v))
}
