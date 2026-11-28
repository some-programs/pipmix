package pipmix

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
	"github.com/some-programs/pipmix/internal/tui"
)

func parseOptions(args ...string) (options, error) {
	fs := flag.NewFlagSet("pipmix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := addFlags(fs)
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return v.check()
}

func TestFlags(t *testing.T) {
	o, err := parseOptions()
	if err != nil || o != defaultOptions() {
		t.Errorf("no flags: %+v, %v; want the defaults %+v", o, err, defaultOptions())
	}
	o, err = parseOptions("-meter.always", "-meter.rate", "20", "-meter.mode", "peak", "-meter.look", "dot", "-meter.speed", "slowest",
		"-meter.peak-hold", "0", "-ui.fps", "30", "-mpris.enable=false", "-dedup.enable=false", "-volume.step", "2.5", "-volume.fine-step", "0.5",
		"-volume.max", "100", "-ui.tab", "outputs", "-mouse.enable=false", "-jack.enable=false",
		"-default.recording", "all", "-default.outputs", "active", "-default.inputs", "all")
	want := options{
		meters: metersAlways, meterRate: 20, meterMode: modePeak, meterLook: meterDot, smoothing: 4, fps: 30,
		step: 0.025, fineStep: 0.005, limit: 1, tab: tabOutputs, recording: showAll, outputs: showActive, inputs: showAll,
	}
	if err != nil || o != want {
		t.Errorf("all flags: %+v, %v; want %+v", o, err, want)
	}
	if o, err := parseOptions("-ui.tab", "6"); err != nil || o.tab != tabCards {
		t.Errorf("-ui.tab 6: tab %d, %v; want Cards", o.tab, err)
	}
	if o, err := parseOptions("-meter.enable=false"); err != nil || o.meters != metersOff {
		t.Errorf("-meter.enable=false: %+v, %v", o, err)
	}
	for _, args := range [][]string{
		{"-meter.enable=false", "-meter.always"},
		{"-meter.rate", "0"},
		{"-meter.mode", "rms"},
		{"-meter.peak-hold", "-1s"},
		{"-ui.fps", "0"},
		{"-volume.max", "50"},
		{"-volume.step", "0"},
		{"-volume.fine-step", "-1"},
		{"-ui.tab", "7"},
	} {
		if _, err := parseOptions(args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestMetersAlways(t *testing.T) {
	o := defaultOptions()
	o.meters = metersAlways
	m := newMixer(pw.NewClient(nil), nil, pulse.NewMeter(nil, 0), quitKeys(), o)
	m.snap = meterSnapshot()
	m.setSize(60, 20)
	m.update(tui.BlurEvent{})
	if got := m.meterTargets(); len(got) == 0 {
		t.Error("-meter.always: no targets while not focused")
	}

	m.handleKey("A")
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("A from always: targets while not focused %v, want none", targetKeys(got))
	}
	m.handleKey("A")
	if got := m.meterTargets(); len(got) == 0 {
		t.Error("A back to always: no targets while not focused")
	}

	// -meter.enable=false: no meter is drawn or metered, focused or not, and
	// the keys that turn meters on leave them off.
	o.meters = metersOff
	m = newMixer(pw.NewClient(nil), nil, pulse.NewMeter(nil, 0), quitKeys(), o)
	m.snap = meterSnapshot()
	m.setSize(60, 20)
	m.handleKey("a")
	m.handleKey("A")
	if got := m.meterTargets(); len(got) != 0 {
		t.Errorf("-meter.enable=false: targets %v, want none", targetKeys(got))
	}
	if got := m.look(); got != meterOff {
		t.Errorf("-meter.enable=false: look %v, want meterOff", got)
	}
}

func TestOptionsInUI(t *testing.T) {
	o := defaultOptions()
	o.step, o.fineStep, o.limit = 0.02, 0.005, 1
	o.jack, o.mouse = false, false
	o.tab, o.meterMode, o.smoothing, o.peakHold = tabPlayback, modeAverage, 1, 0
	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), o)
	m.snap = meterSnapshot()
	m.setSize(120, 40)
	m.refocus()
	if m.tab != tabPlayback || m.meterMode != modeAverage || m.smoothing != 1 {
		t.Errorf("starts on tab %d, mode %d, smoothing %d; want %d, %d, 1", m.tab, m.meterMode, m.smoothing, tabPlayback, modeAverage)
	}
	for _, it := range m.items(tabPlayback) {
		if it.node.JACK {
			t.Errorf("-jack.disable lists %s", it.title())
		}
	}
	m.handleKey("?")
	view := ansi.Strip(m.view())
	for _, want := range []string{"Louder by 2%", "Quieter by 0.5%"} {
		if !strings.Contains(view, want) {
			t.Errorf("help lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "click") {
		t.Errorf("help without the mouse has mouse actions:\n%s", view)
	}
	if got := terminalOptions(m.opts).Mouse; got {
		t.Error("-mouse.disable: the terminal reports the mouse")
	}
	// A full fader is the limit.
	if k := knobCell(1, o.limit, 16); k != 15 {
		t.Errorf("knob of 100%% with -volume.max 100 in cell %d, want 15", k)
	}
}
