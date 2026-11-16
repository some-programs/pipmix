package mpris

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestParseStat(t *testing.T) {
	for _, tt := range []struct {
		stat string
		ppid int
		ok   bool
	}{
		{"27046 (chrome) S 25898 25898 1 0 -1", 25898, true},
		{"42 (a (b) c) R 7 42 1", 7, true},
		{"42 (x", 0, false},
		{"42 (x) S", 0, false},
	} {
		ppid, ok := parseStat(tt.stat)
		if ppid != tt.ppid || ok != tt.ok {
			t.Errorf("parseStat(%q) = %d, %v, want %d, %v", tt.stat, ppid, ok, tt.ppid, tt.ok)
		}
	}
}

func TestPlayer(t *testing.T) {
	parents := map[int]int{300: 200, 200: 100, 100: 1, 50: 1}
	parent := func(pid int) (int, bool) {
		ppid, ok := parents[pid]
		return ppid, ok
	}
	s := &Snapshot{Players: []Player{
		{Name: "org.mpris.MediaPlayer2.a", PID: 200, Title: "Paused"},
		{Name: "org.mpris.MediaPlayer2.b", PID: 200, Title: "Playing", Playing: true},
		{Name: "org.mpris.MediaPlayer2.c", PID: 200, Title: "Paused too"},
		{Name: "org.mpris.MediaPlayer2.d", PID: 100, Title: "Grandparent"},
		{Name: "org.mpris.MediaPlayer2.e", PID: 50},
	}}
	for _, tt := range []struct {
		pid  int
		want string
		ok   bool
	}{
		{300, "Playing", true},
		{200, "Playing", true},
		{100, "Grandparent", true},
		{50, "", true},
		{999, "", false},
	} {
		if p, ok := s.player(tt.pid, parent); ok != tt.ok || p.Title != tt.want {
			t.Errorf("player(%d) = %+v, %v; want %q, %v", tt.pid, p, ok, tt.want, tt.ok)
		}
	}
}

func TestApply(t *testing.T) {
	var p player
	p.apply(map[string]dbus.Variant{
		"Metadata": dbus.MakeVariant(map[string]dbus.Variant{
			"xesam:title":  dbus.MakeVariant("A\x1b[31m title\n\twith  spaces "),
			"xesam:artist": dbus.MakeVariant([]string{"Someone"}),
		}),
		"PlaybackStatus": dbus.MakeVariant("Playing"),
	})
	if p.Title != "A [31m title with spaces" || !p.Playing {
		t.Errorf("after apply: %q, playing %v", p.Title, p.Playing)
	}
	p.apply(map[string]dbus.Variant{"PlaybackStatus": dbus.MakeVariant("Paused")})
	if p.Title == "" || p.Playing {
		t.Errorf("status only: %q, playing %v", p.Title, p.Playing)
	}
	p.apply(map[string]dbus.Variant{"Metadata": dbus.MakeVariant(map[string]dbus.Variant{})})
	if p.Title != "" {
		t.Errorf("no title in metadata: %q", p.Title)
	}
}
