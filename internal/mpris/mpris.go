// Package mpris watches MPRIS media players on the D-Bus session bus.
package mpris

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/godbus/dbus/v5"
)

const (
	minBackoff  = time.Second
	maxBackoff  = 30 * time.Second
	callTimeout = 2 * time.Second
)

const (
	namePrefix   = "org.mpris.MediaPlayer2"
	playerPath   = "/org/mpris/MediaPlayer2"
	playerIface  = "org.mpris.MediaPlayer2.Player"
	busName      = "org.freedesktop.DBus"
	propertiesIf = "org.freedesktop.DBus.Properties"
)

// A Player is an MPRIS media player.
type Player struct {
	Name    string
	PID     int
	Title   string
	Playing bool
}

// A Snapshot is the players at one moment.
type Snapshot struct {
	Connected bool
	Err       error
	Players   []Player // ordered by Name
}

const maxDepth = 32

// Player returns the player for pid or its closest ancestor, preferring a
// playing player.
func (s *Snapshot) Player(pid int) (Player, bool) {
	return s.player(pid, parentPID)
}

func (s *Snapshot) player(pid int, parent func(int) (int, bool)) (Player, bool) {
	if len(s.Players) == 0 {
		return Player{}, false
	}
	for range maxDepth {
		var (
			matched Player
			found   bool
		)
		for _, p := range s.Players {
			if p.PID != pid {
				continue
			}
			if !found || (!matched.Playing && (p.Playing || (matched.Title == "" && p.Title != ""))) {
				matched = p
				found = true
			}
		}
		if found {
			return matched, true
		}
		var ok bool
		if pid, ok = parent(pid); !ok || pid <= 1 {
			return Player{}, false
		}
	}
	return Player{}, false
}

func parentPID(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return parseStat(string(b))
}

// parseStat extracts ppid from a /proc/<pid>/stat line ("pid (comm) state ppid ...").
func parseStat(stat string) (int, bool) {
	_, rest, ok := strings.CutLast(stat, ")")
	if !ok {
		return 0, false
	}
	f := strings.Fields(rest)
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil
}

// A Watcher tracks MPRIS players on the session bus.
type Watcher struct {
	log     *log.Logger
	snap    atomic.Pointer[Snapshot]
	updated chan struct{}
	conn    atomic.Pointer[dbus.Conn]
}

// NewWatcher returns a watcher that logs to logger.
func NewWatcher(logger *log.Logger) *Watcher {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	w := &Watcher{log: logger, updated: make(chan struct{}, 1)}
	w.snap.Store(&Snapshot{})
	return w
}

// Snapshot returns the latest players.
func (w *Watcher) Snapshot() *Snapshot { return w.snap.Load() }

// Updated returns a channel that receives when a new snapshot is out.
// Updates in quick succession may arrive as one.
func (w *Watcher) Updated() <-chan struct{} { return w.updated }

func (w *Watcher) publish(s *Snapshot) {
	w.snap.Store(s)
	select {
	case w.updated <- struct{}{}:
	default:
	}
}

// PlayPause asks the player with bus name to toggle playback.
func (w *Watcher) PlayPause(name string) error {
	conn := w.conn.Load()
	if conn == nil {
		return errors.New("no session bus")
	}
	return call(context.Background(), conn.Object(name, playerPath), playerIface+".PlayPause", nil)
}

// Run watches the players until ctx is done, reconnecting when the bus
// connection is lost.
func (w *Watcher) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		start := time.Now()
		err := w.watch(ctx)
		if ctx.Err() != nil {
			return
		}
		w.log.Printf("mpris: %v", err)
		w.publish(&Snapshot{Err: err})
		if time.Since(start) > time.Minute {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

type player struct {
	Player
	owner string
}

func (w *Watcher) watch(ctx context.Context) error {
	addr, err := SessionBus()
	if err != nil {
		return err
	}
	conn, err := dbus.Connect(addr, dbus.WithContext(ctx), dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
	if err != nil {
		return err
	}
	defer conn.Close()
	w.conn.Store(conn)
	defer w.conn.Store(nil)

	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	err = conn.AddMatchSignalContext(ctx,
		dbus.WithMatchSender(busName), dbus.WithMatchInterface(busName),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg0Namespace(namePrefix))
	if err != nil {
		return err
	}
	err = conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(propertiesIf), dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchObjectPath(playerPath), dbus.WithMatchArg(0, playerIface))
	if err != nil {
		return err
	}

	var names []string
	if err := call(ctx, conn.BusObject(), busName+".ListNames", &names); err != nil {
		return err
	}
	players := make(map[string]*player)
	for _, name := range names {
		if isPlayer(name) {
			w.add(ctx, conn, players, name, "")
		}
	}
	w.publish(snapshot(players))

	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				return errors.New("bus connection closed")
			}
			if w.handle(ctx, conn, players, sig) {
				w.publish(snapshot(players))
			}
		}
	}
}

// SessionBus returns the D-Bus session bus address from the environment.
func SessionBus() (string, error) {
	if addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" && addr != "autolaunch:" {
		return addr, nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		path := dir + "/bus"
		if _, err := os.Stat(path); err == nil {
			return "unix:path=" + dbus.EscapeBusAddressValue(path), nil
		}
	}
	return "", errors.New("no session bus")
}

func isPlayer(name string) bool { return strings.HasPrefix(name, namePrefix+".") }

// handle applies a signal to players and reports whether they changed.
func (w *Watcher) handle(ctx context.Context, conn *dbus.Conn, players map[string]*player, sig *dbus.Signal) bool {
	switch sig.Name {
	case busName + ".NameOwnerChanged":
		var name, oldOwner, newOwner string
		if dbus.Store(sig.Body, &name, &oldOwner, &newOwner) != nil || !isPlayer(name) {
			return false
		}
		delete(players, name)
		if newOwner != "" {
			w.add(ctx, conn, players, name, newOwner)
		}
		return true
	case propertiesIf + ".PropertiesChanged":
		var iface string
		var changed map[string]dbus.Variant
		var invalidated []string
		if dbus.Store(sig.Body, &iface, &changed, &invalidated) != nil || iface != playerIface {
			return false
		}
		found := false
		for _, p := range players {
			if p.owner != sig.Sender {
				continue
			}
			p.apply(changed)
			if len(invalidated) > 0 {
				w.fetch(ctx, conn, p)
			}
			found = true
		}
		return found
	}
	return false
}

// add adds the player with bus name, owned by owner, or by whoever owns it
// now if owner is "".
func (w *Watcher) add(ctx context.Context, conn *dbus.Conn, players map[string]*player, name, owner string) {
	bus := conn.BusObject()
	if owner == "" {
		if err := call(ctx, bus, busName+".GetNameOwner", &owner, name); err != nil {
			w.log.Printf("mpris: owner of %s: %v", name, err)
			return
		}
	}
	var pid uint32
	if err := call(ctx, bus, busName+".GetConnectionUnixProcessID", &pid, owner); err != nil {
		w.log.Printf("mpris: pid of %s: %v", name, err)
		return
	}
	p := &player{Name: name, PID: int(pid), owner: owner}
	w.fetch(ctx, conn, p)
	players[name] = p
}

// fetch reads p's metadata and playback status.
func (w *Watcher) fetch(ctx context.Context, conn *dbus.Conn, p *player) {
	var props map[string]dbus.Variant
	if err := call(ctx, conn.Object(p.owner, playerPath), propertiesIf+".GetAll", &props, playerIface); err != nil {
		w.log.Printf("mpris: properties of %s: %v", p.Name, err)
		return
	}
	p.apply(props)
}

// apply takes the Metadata and PlaybackStatus in props, if there.
func (p *player) apply(props map[string]dbus.Variant) {
	if v, ok := props["Metadata"]; ok {
		md, _ := v.Value().(map[string]dbus.Variant)
		title, _ := md["xesam:title"].Value().(string)
		p.Title = clean(title)
	}
	if v, ok := props["PlaybackStatus"]; ok {
		status, _ := v.Value().(string)
		p.Playing = status == "Playing"
	}
}

// clean collapses whitespace and control characters in s into single spaces.
func clean(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

func snapshot(players map[string]*player) *Snapshot {
	s := &Snapshot{Connected: true}
	for _, p := range players {
		s.Players = append(s.Players, p.Player)
	}
	slices.SortFunc(s.Players, func(a, b Player) int { return cmp.Compare(a.Name, b.Name) })
	return s
}

// call calls method on obj with a timeout and stores the reply in ret.
func call(ctx context.Context, obj dbus.BusObject, method string, ret any, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	c := obj.CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		return c.Err
	}
	if ret != nil {
		return c.Store(ret)
	}
	return nil
}
