package pipmix

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/some-programs/pipmix/internal/pulse"
)

type options struct {
	meters                meterWhen
	meterRate             int
	meterMode             meterMode
	meterLook             meterLook
	smoothing             int
	peakHold              time.Duration
	fps                   int
	titles                bool
	dedup                 bool
	step, fineStep, limit float64 // cubic volume (1 is 100%)
	tab                   tab
	mouse                 bool
	jack                  bool
	recording             showWhen
	outputs, inputs       showWhen
}

func defaultOptions() options {
	return options{
		meters:    metersFocused,
		meterRate: pulse.DefaultRate,
		smoothing: defaultSmoothing,
		peakHold:  time.Second,
		fps:       60,
		titles:    true,
		dedup:     true,
		step:      0.05,
		fineStep:  0.01,
		limit:     1.5,
		mouse:     true,
		jack:      true,
		recording: showActive,
	}
}

// A showWhen says which nodes of a kind the Default tab shows.
type showWhen int

const (
	showNone   showWhen = iota // none, or only the default output
	showActive                 // those active, and for a while after
	showAll                    // all
)

var (
	showFlags   = []string{"off", "active", "all"}
	outputFlags = []string{"default", "active", "all"}
)

type meterWhen int

const (
	metersFocused meterWhen = iota
	metersAlways
	metersOff
)

var meterModeFlags = []string{"both", "average", "peak"}

var tabFlags = [tabCount]string{"default", "playback", "recording", "outputs", "inputs", "cards"}

var smoothingNames = func() []string {
	names := make([]string, len(smoothings))
	for i, s := range smoothings {
		names[i] = s.name
	}
	return names
}()

// addFlags registers pipmix's flags on fs.
func addFlags(fs *flag.FlagSet) *flagValues {
	o := defaultOptions()
	v := &flagValues{
		options:     o,
		step:        o.step * 100,
		fineStep:    o.fineStep * 100,
		limit:       o.limit * 100,
		meterEnable: o.meters != metersOff,
		mprisEnable: o.titles,
		dedupEnable: o.dedup,
		mouseEnable: o.mouse,
		jackEnable:  o.jack,
	}
	fs.String("config", defaultConfigPath(), "read config from `file`")
	fs.BoolVar(&v.meterEnable, "meter.enable", v.meterEnable, "draw level meters; if false, pipewire-pulse is only used for hide rules with when = \"silent\"")
	fs.BoolVar(&v.meterAlways, "meter.always", v.meterAlways, "meter while the terminal isn't focused too")
	fs.IntVar(&v.meterRate, "meter.rate", v.meterRate, "peaks a second per meter, 1 to 200; above ~46 PipeWire may shorten its quantum for all audio")
	fs.Var(&choice[meterMode]{&v.meterMode, meterModeFlags}, "meter.mode", "meter `mode`: both (the average, and a cell at the peak), average, or peak")
	fs.Var(&choice[meterLook]{&v.meterLook, meterLookFlags}, "meter.look", "meter `look`: solid, shade (solid on a lighter shade), or dot")
	fs.Var(&choice[int]{&v.smoothing, smoothingNames}, "meter.speed", "meter `speed`, how fast they follow the music: "+strings.Join(smoothingNames, ", "))
	fs.DurationVar(&v.peakHold, "meter.peak-hold", v.peakHold, "how long a meter's peak cell holds before it falls")
	fs.BoolVar(&v.mprisEnable, "mpris.enable", v.mprisEnable, "read MPRIS players on the session bus, which title playback streams with what they play")
	fs.BoolVar(&v.dedupEnable, "dedup.enable", v.dedupEnable, "hide mono playback streams that duplicate a multi-channel stream")
	fs.Float64Var(&v.step, "volume.step", v.step, "volume step of h and l, in `percent`")
	fs.Float64Var(&v.fineStep, "volume.fine-step", v.fineStep, "volume step of H and L, in `percent`")
	fs.Float64Var(&v.limit, "volume.max", v.limit, "most a volume can be raised to, in `percent`, 100 to 400")
	fs.IntVar(&v.fps, "ui.fps", v.fps, "most redraws a second, 1 to 240; the meters move at this rate")
	fs.Var(tabFlag{&v.tab}, "ui.tab", "`tab` to start on: "+strings.Join(tabFlags[:], ", ")+", or 1 to 6")
	fs.BoolVar(&v.mouseEnable, "mouse.enable", v.mouseEnable, "use the mouse; disable to let terminal selection work")
	fs.BoolVar(&v.jackEnable, "jack.enable", v.jackEnable, "list JACK clients")
	fs.Var(&choice[showWhen]{&v.recording, showFlags}, "default.recording", "recording streams the Default tab shows, at the bottom: off, active (while recording, and a few seconds after), or all; JACK clients only show on the Recording tab")
	fs.Var(&choice[showWhen]{&v.outputs, outputFlags}, "default.outputs", "output devices the Default tab shows: default, active (also others while in use, and a few seconds after), or all")
	fs.Var(&choice[showWhen]{&v.inputs, showFlags}, "default.inputs", "input devices the Default tab shows, above recording streams: off, active (those in use, and a few seconds after), or all")
	return v
}

type flagValues struct {
	options
	step, fineStep, limit float64
	meterEnable           bool
	meterAlways           bool
	mprisEnable           bool
	dedupEnable           bool
	mouseEnable           bool
	jackEnable            bool
}

func (v *flagValues) check() (options, error) {
	o := v.options
	switch {
	case !v.meterEnable && v.meterAlways:
		return o, errors.New("-meter.enable=false and -meter.always: choose one")
	case o.meterRate < 1 || o.meterRate > 200:
		return o, fmt.Errorf("-meter.rate %d: want 1 to 200", o.meterRate)
	case o.fps < 1 || o.fps > 240:
		return o, fmt.Errorf("-ui.fps %d: want 1 to 240", o.fps)
	case o.peakHold < 0:
		return o, fmt.Errorf("-meter.peak-hold %v: want 0 or more", o.peakHold)
	case v.limit < 100 || v.limit > 400:
		return o, fmt.Errorf("-volume.max %g: want 100 to 400", v.limit)
	case v.step <= 0 || v.step > v.limit:
		return o, fmt.Errorf("-volume.step %g: want more than 0, up to -volume.max", v.step)
	case v.fineStep <= 0 || v.fineStep > v.limit:
		return o, fmt.Errorf("-volume.fine-step %g: want more than 0, up to -volume.max", v.fineStep)
	}
	o.step, o.fineStep, o.limit = v.step/100, v.fineStep/100, v.limit/100
	switch {
	case !v.meterEnable:
		o.meters = metersOff
	case v.meterAlways:
		o.meters = metersAlways
	}
	o.titles, o.dedup = v.mprisEnable, v.dedupEnable
	o.mouse, o.jack = v.mouseEnable, v.jackEnable
	return o, nil
}

// A choice is a flag that takes one of names, and sets *v to its index.
type choice[T ~int] struct {
	v     *T
	names []string
}

func (c *choice[T]) String() string {
	if c.v == nil || int(*c.v) >= len(c.names) {
		return ""
	}
	return c.names[*c.v]
}

func (c *choice[T]) Set(s string) error {
	i := slices.Index(c.names, s)
	if i < 0 {
		return fmt.Errorf("want one of %s", strings.Join(c.names, ", "))
	}
	*c.v = T(i)
	return nil
}

// A tabFlag takes a tab's name or number.
type tabFlag struct{ t *tab }

func (f tabFlag) String() string {
	if f.t == nil {
		return ""
	}
	return tabFlags[*f.t]
}

func (f tabFlag) Set(s string) error {
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= int(tabCount) {
		*f.t = tab(n - 1)
		return nil
	}
	i := slices.Index(tabFlags[:], strings.ToLower(s))
	if i < 0 {
		return fmt.Errorf("want one of %s, or 1 to %d", strings.Join(tabFlags[:], ", "), tabCount)
	}
	*f.t = tab(i)
	return nil
}

// percentText formats volume v as a percentage, e.g. "5%".
func percentText(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e4)/100, 'f', -1, 64) + "%"
}
