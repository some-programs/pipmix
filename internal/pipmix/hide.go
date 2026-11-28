package pipmix

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/some-programs/pipmix/internal/pw"
)

// defaultHideAfter is how long a rule's condition must hold, unless the
// rule sets after.
const defaultHideAfter = 10 * time.Second

// defaultThreshold is the level, in dBFS, below which a silent rule
// counts a node as silent, unless the rule sets threshold. It is just above
// the dither noise of 16-bit audio, about -90 dBFS.
const defaultThreshold = -80.0

// A hideRule is a [[default.hide]] table: it hides the nodes it matches
// from the Default tab while its condition holds.
type hideRule struct {
	Match     nodeMatch     `toml:"match"`
	When      hideWhen      `toml:"when"`
	After     *tomlDuration `toml:"after"`
	Threshold *float64      `toml:"threshold"` // dBFS

	after     time.Duration
	threshold float64 // dBFS
	level     float64 // threshold as a linear peak
}

// A hideWhen is the condition under which a rule hides a node.
type hideWhen int

const (
	hideAlways hideWhen = iota // always
	hideIdle                   // the node isn't active
	hideSilent                 // the node's peaks stay below the threshold
	hideMuted                  // the node is muted; 0% isn't enough
)

var hideWhenNames = []string{"always", "idle", "silent", "muted"}

func (w hideWhen) String() string { return hideWhenNames[w] }

func (w *hideWhen) UnmarshalText(b []byte) error {
	i := slices.Index(hideWhenNames, string(b))
	if i < 0 {
		return fmt.Errorf("when %q: want one of %s", b, strings.Join(hideWhenNames, ", "))
	}
	*w = hideWhen(i)
	return nil
}

// A tomlDuration is a time.Duration written as a string, such as "30s".
type tomlDuration time.Duration

func (d *tomlDuration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("duration %q: want 0 or more", b)
	}
	*d = tomlDuration(v)
	return nil
}

// A nodeMatch selects nodes. Each field set must match; Type matches the
// node's kind and the others are unanchored regexps on node properties.
type nodeMatch struct {
	Type        *nodeType      `toml:"type"`
	Name        *regexp.Regexp `toml:"name"`        // node.name
	Description *regexp.Regexp `toml:"description"` // node.description
	App         *regexp.Regexp `toml:"app"`         // application.name, or the process name of an ALSA client
	Binary      *regexp.Regexp `toml:"binary"`      // application.process.binary
}

// A matchProp pairs a nodeMatch field with its key and node property.
type matchProp struct {
	key, prop string
	re        *regexp.Regexp
}

func (m nodeMatch) props() [4]matchProp {
	return [...]matchProp{
		{"name", "node.name", m.Name},
		{"description", "node.description", m.Description},
		{"app", "application.name", m.App},
		{"binary", "application.process.binary", m.Binary},
	}
}

func (m nodeMatch) matches(n *pw.Node) bool {
	if m.Type != nil && pw.Class(*m.Type) != n.Class {
		return false
	}
	for _, p := range m.props() {
		if p.re == nil || p.re.MatchString(n.Props[p.prop]) {
			continue
		}
		if alt := n.Props[pw.PropProcessName]; p.key == "app" && alt != "" && p.re.MatchString(alt) {
			continue
		}
		return false
	}
	return true
}

func (m nodeMatch) String() string {
	var parts []string
	if m.Type != nil {
		parts = append(parts, fmt.Sprintf("type = %q", m.Type))
	}
	for _, p := range m.props() {
		if p.re != nil {
			parts = append(parts, fmt.Sprintf("%s = %q", p.key, p.re))
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// A nodeType is the kind of node a rule matches, as written in the config.
type nodeType pw.Class

// nodeTypeNames are the config's names for pw.Class values, in order.
var nodeTypeNames = []string{"playback", "recording", "output", "input"}

func (t nodeType) String() string { return nodeTypeNames[t] }

func (t *nodeType) UnmarshalText(b []byte) error {
	i := slices.Index(nodeTypeNames, string(b))
	if i < 0 {
		return fmt.Errorf("type %q: want one of %s", b, strings.Join(nodeTypeNames, ", "))
	}
	*t = nodeType(i)
	return nil
}

func (r hideRule) String() string {
	s := fmt.Sprintf("match = %v, when = %q", r.Match, r.When)
	if r.When != hideAlways {
		s += fmt.Sprintf(", after = %q", r.after)
	}
	if r.When == hideSilent {
		s += fmt.Sprintf(", threshold = %g", r.threshold)
	}
	return s
}

// loadHideRules reads the [[default.hide]] rules from the config file at
// path, which may be missing.
func loadHideRules(path string) ([]hideRule, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Default struct {
			Hide []hideRule `toml:"hide"`
		} `toml:"default"`
	}
	d := toml.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	err = d.Decode(&cfg)
	if se, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		// Keys outside [[default.hide]] belong to flags, checked already.
		err = nil
		for _, e := range se.Errors {
			if k := e.Key(); len(k) > 2 && k[0] == "default" && k[1] == "hide" {
				row, col := e.Position()
				return nil, fmt.Errorf("config %d:%d: unknown key %s in [[default.hide]]\n%s", row, col, strings.Join(k[2:], "."), e.String())
			}
		}
	}
	if de, ok := errors.AsType[*toml.DecodeError](err); ok {
		row, col := de.Position()
		return nil, fmt.Errorf("config %d:%d: %s\n%s", row, col, de.Error(), de.String())
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	rules := cfg.Default.Hide
	for i := range rules {
		if err := rules[i].check(); err != nil {
			return nil, fmt.Errorf("config: [[default.hide]] number %d: %w", i+1, err)
		}
	}
	return rules, nil
}

// check validates r and fills in its defaults.
func (r *hideRule) check() error {
	switch {
	case r.Match == nodeMatch{}:
		return errors.New("match: want at least one of type, name, description, app, binary")
	case r.When == hideAlways && r.After != nil:
		return errors.New(`after: not used with when = "always"`)
	case r.When != hideSilent && r.Threshold != nil:
		return errors.New(`threshold: only used with when = "silent"`)
	case r.Threshold != nil && *r.Threshold > 0:
		return fmt.Errorf("threshold %g: want 0 dBFS or less", *r.Threshold)
	}
	r.after = defaultHideAfter
	if r.After != nil {
		r.after = time.Duration(*r.After)
	}
	r.threshold = defaultThreshold
	if r.Threshold != nil {
		r.threshold = *r.Threshold
	}
	r.level = math.Pow(10, r.threshold/20)
	return nil
}
