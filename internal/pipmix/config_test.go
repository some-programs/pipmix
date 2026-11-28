package pipmix

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, text string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func parseAll(args ...string) (options, *debugFlags, error) {
	fs := flag.NewFlagSet("pipmix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	d := addDebugFlags(fs)
	v := addFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return options{}, nil, err
	}
	o, err := v.check()
	return o, d, err
}

func TestConfig(t *testing.T) {
	p := writeConfigFile(t, `
[volume]
step = 2.5
fine-step = 0.5
max = 120.0

[ui]
fps = 30
tab = "outputs"

[meter]
always = true
rate = 25
mode = "peak"
look = "dot"
speed = "slowest"
peak-hold = "500ms"

[mpris]
enable = false

[dedup]
enable = false

[mouse]
enable = false

[jack]
enable = false

[default]
recording = "off"
outputs = "all"
inputs = "active"

[[default.hide]] # read by loadHideRules, not as flags
match = { name = "x" }

[debug]
log = true
log-file = "/tmp/pipmix-test.log"
http = "localhost:6060"
dump = true
`)
	o, d, err := parseAll("-config", p)
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		meters:    metersAlways,
		meterRate: 25,
		meterMode: modePeak,
		meterLook: meterDot,
		smoothing: 4, // slowest
		peakHold:  500 * time.Millisecond,
		fps:       30,
		step:      0.025,
		fineStep:  0.005,
		limit:     1.2,
		tab:       tabOutputs,
		recording: showNone,
		outputs:   showAll,
		inputs:    showActive,
	}
	if o != want {
		t.Errorf("options:\ngot  %+v\nwant %+v", o, want)
	}
	wantDebug := debugFlags{log: true, logFile: "/tmp/pipmix-test.log", http: "localhost:6060", dump: true}
	if *d != wantDebug {
		t.Errorf("debug = %+v, want %+v", *d, wantDebug)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if _, _, err := parseAll(); err != nil {
		t.Errorf("no default config: %v, want no error", err)
	}
	if _, _, err := parseAll("-config", filepath.Join(dir, "missing.toml")); err != nil {
		t.Errorf("-config naming a missing file: %v, want no error", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "pipmix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultConfigPath(), []byte("[ui]\nfps = 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, _, err := parseAll(); err != nil || o.fps != 30 {
		t.Errorf("default config: fps %d, %v; want 30", o.fps, err)
	}
	other := filepath.Join(dir, "other.toml")
	if err := os.WriteFile(other, []byte("[ui]\nfps = 40\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, _, err := parseAll("-config", other); err != nil || o.fps != 40 {
		t.Errorf("-config naming another file: fps %d, %v; want 40", o.fps, err)
	}
}

func TestConfigErrors(t *testing.T) {
	for _, tc := range []struct {
		name, toml, want string
	}{
		{"syntax error", "[volume\n", "config 1:"},
		{"unknown key", "[volume]\nstpe = 5\n", `"volume.stpe" not defined`},
		{"hide rule in single brackets", "[default.hide]\nmatch = { name = \"x\" }\n", "write [[default.hide]], with double brackets"},
	} {
		p := writeConfigFile(t, tc.toml)
		_, _, err := parseAll("-config", p)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestConfigPrecedence(t *testing.T) {
	p := writeConfigFile(t, `
[volume]
step = 2.0
max = 120.0

[meter]
rate = 20
always = true

[ui]
fps = 30
`)
	t.Setenv("PIPMIX_UI_FPS", "50")
	t.Setenv("PIPMIX_VOLUME_STEP", "3")
	o, _, err := parseAll("-config", p, "-volume.step", "7.0", "-meter.always=false")
	if err != nil {
		t.Fatal(err)
	}
	if o.step != 0.07 || o.meters != metersFocused {
		t.Errorf("step %v, meters %v; want the command line's 0.07 and metersFocused", o.step, o.meters)
	}
	if o.fps != 50 {
		t.Errorf("fps %d, want the environment's 50", o.fps)
	}
	if o.limit != 1.2 || o.meterRate != 20 {
		t.Errorf("limit %v, rate %d; want the config's 1.2 and 20", o.limit, o.meterRate)
	}
}

func TestOpenDebugLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f, err := openDebugLog("")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pipmix")
	if filepath.Dir(f.Name()) != dir || !strings.HasSuffix(f.Name(), fmt.Sprintf("-%d.log", os.Getpid())) {
		t.Errorf("log at %s, want one in %s ending with the pid", f.Name(), dir)
	}

	path := filepath.Join(t.TempDir(), "chosen.log")
	f, err = openDebugLog(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if f.Name() != path {
		t.Errorf("log at %s, want %s", f.Name(), path)
	}
}

func TestVersionCommandLineOnly(t *testing.T) {
	t.Setenv("PIPMIX_VERSION", "1.4.0")
	t.Setenv("PIPMIX_VERSION_INFO", "true")
	args := []string{"-config", filepath.Join(t.TempDir(), "none.toml")}
	fs := flag.NewFlagSet("pipmix", flag.ContinueOnError)
	v := addVersionFlags(fs)
	addFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	v.lock()
	if err := parseFlags(fs, args); err != nil {
		t.Fatalf("PIPMIX_VERSION in the environment: %v", err)
	}
	if v.print(io.Discard) {
		t.Error("the environment set -version")
	}
}
