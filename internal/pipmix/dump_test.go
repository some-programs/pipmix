package pipmix

import (
	"bytes"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/some-programs/pipmix/internal/mpris"
	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

func TestDumpNotConnected(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("PIPEWIRE_REMOTE", missing)
	t.Setenv("PULSE_SERVER", "unix:"+missing)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+missing)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fs := flag.NewFlagSet("pipmix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	v := addFlags(fs)
	if err := parseFlags(fs, nil); err != nil {
		t.Fatal(err)
	}
	opts, err := v.check()
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	client := pw.NewClient(nil)
	go client.Run(ctx)
	media := mpris.NewWatcher(nil)
	go media.Run(ctx)
	meter := pulse.NewMeter(nil, 0)
	go meter.Run(ctx)

	var out bytes.Buffer
	err = dump(ctx, &out, &logBuffer{}, fs, opts, client, media, meter)
	for _, want := range []string{"[pipewire]\nnot connected: ", "[mpris]\nnot connected: ", "[pulse]\nnot connected: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dump lacks %q:\n%s", want, out.String())
		}
	}
	if err == nil || err.Error() != "pipewire, mpris, pulse not connected" {
		t.Errorf("err = %v, want all three not connected", err)
	}
}
