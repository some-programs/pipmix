package pipmix

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/some-programs/pipmix/internal/pw"
)

func TestLoadHideRules(t *testing.T) {
	p := writeConfigFile(t, `
[ui]
fps = 30

[default]
recording = "all"

[[default.hide]]
match = { type = "playback", name = "^FluidSynth$" }
when = "muted"
after = "30s"

[[default.hide]]
match = { binary = "firefox" }
when = "idle"

[[default.hide]]
match = { description = "HDMI" }
`)
	rules, err := loadHideRules(p) // ignoring the flags' tables
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rules {
		got = append(got, r.String())
	}
	want := []string{
		`match = { type = "playback", name = "^FluidSynth$" }, when = "muted", after = "30s"`,
		`match = { binary = "firefox" }, when = "idle", after = "10s"`,
		`match = { description = "HDMI" }, when = "always"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("rules:\ngot  %q\nwant %q", got, want)
	}

	if rules, err := loadHideRules(p + ".missing"); rules != nil || err != nil {
		t.Errorf("missing file: %v, %v; want no rules and no error", rules, err)
	}
}

func TestLoadHideRulesErrors(t *testing.T) {
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"unknown key", "[[default.hide]]\nmatch = { nmae = \"x\" }\n", "config 2:11: unknown key nmae in [[default.hide]]"},
		{"unknown rule key", "[[default.hide]]\nmatch = { name = \"x\" }\nwen = \"idle\"\n", "config 3:1: unknown key wen"},
		{"bad regexp", "[[default.hide]]\nmatch = { name = \"(x\" }\n", "config 2:18: toml: error parsing regexp"},
		{"bad type", "[[default.hide]]\nmatch = { type = \"sink\" }\n", `type "sink": want one of playback, recording, output, input`},
		{"bad when", "[[default.hide]]\nmatch = { name = \"x\" }\nwhen = \"quiet\"\n", `when "quiet": want one of always, idle, muted`},
		{"bad after", "[[default.hide]]\nmatch = { name = \"x\" }\nwhen = \"idle\"\nafter = \"-1s\"\n", `want 0 or more`},
		{"empty match", "[[default.hide]]\nwhen = \"idle\"\n", "number 1: match: want at least one of type, name, description, app, binary"},
		{"after with always", "[[default.hide]]\nmatch = { name = \"x\" }\nafter = \"1s\"\n", `after: not used with when = "always"`},
		{"second rule", "[[default.hide]]\nmatch = { name = \"x\" }\n[[default.hide]]\n", "number 2: match"},
	} {
		_, err := loadHideRules(writeConfigFile(t, tc.toml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestNodeMatch(t *testing.T) {
	fluid := nodeMatch{Type: new(nodeType(pw.Playback)), Name: regexp.MustCompile("^FluidSynth$")}
	n := &pw.Node{Class: pw.Playback, Props: map[string]string{"node.name": "FluidSynth"}}
	if !fluid.matches(n) {
		t.Error("type and name don't match FluidSynth")
	}
	n.Class = pw.Sink
	if fluid.matches(n) {
		t.Error("type playback matches an output named FluidSynth")
	}

	m := nodeMatch{App: regexp.MustCompile("^claude$")}
	alsa := &pw.Node{Props: map[string]string{"application.name": "PipeWire ALSA [2.1.332]", pw.PropProcessName: "claude"}}
	if !m.matches(alsa) {
		t.Error("app doesn't match an ALSA client's process name")
	}
	if m.matches(&pw.Node{Props: map[string]string{"application.name": "firefox"}}) {
		t.Error("app matches another application")
	}
	m.App = regexp.MustCompile("ALSA")
	if !m.matches(alsa) {
		t.Error("app doesn't match an ALSA client's application.name")
	}
}
