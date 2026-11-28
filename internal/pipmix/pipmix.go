// Package pipmix implements the pipmix terminal mixer.
package pipmix

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"time"

	"github.com/some-programs/pipmix/internal/mpris"
	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
)

// Run parses flags and config from args and runs the mixer.
func Run(args []string) error {
	fs := flag.NewFlagSet("pipmix", flag.ExitOnError)
	version := addVersionFlags(fs)
	debug := addDebugFlags(fs)
	values := addFlags(fs)
	// The command line alone decides -version, so that PIPMIX_VERSION in the
	// environment doesn't set it and a broken config file doesn't stop it.
	if err := fs.Parse(args); err != nil {
		return err
	}
	if version.print(os.Stdout) {
		return nil
	}
	version.lock()
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	opts, err := values.check()
	if err != nil {
		return err
	}
	hide, err := loadHideRules(fs.Lookup("config").Value.String())
	if err != nil {
		return err
	}
	var clientLog *log.Logger
	if debug.log {
		f, err := openDebugLog(debug.logFile)
		if err != nil {
			return err
		}
		defer f.Close()
		clientLog = log.New(f, "", log.Ltime|log.Lmicroseconds)
		clientLog.Printf("pipmix %d started", os.Getpid())
	}
	var dumpLog logBuffer
	if debug.dump {
		clientLog = log.New(&dumpLog, "", log.Ltime|log.Lmicroseconds)
	}
	if debug.http != "" {
		ln, err := net.Listen("tcp", debug.http)
		if err != nil {
			return err
		}
		go http.Serve(ln, nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var media *mpris.Watcher
	if opts.titles {
		media = mpris.NewWatcher(clientLog)
		go media.Run(ctx)
	}

	// The meter always runs, also with -meter.enable=false, for the hide
	// rules that listen to levels. It connects to pipewire-pulse only while
	// it has a node to measure.
	meter := pulse.NewMeter(clientLog, opts.meterRate)
	go meter.Run(ctx)

	client := pw.NewClient(clientLog)
	meter.JACKReady = client.JackMeterReady
	go client.Run(ctx)
	if debug.dump {
		return dump(ctx, os.Stdout, &dumpLog, fs, opts, hide, client, media, meter)
	}
	m := newMixer(client, media, meter, quitKeys(), opts)
	m.hide = hide
	return run(ctx, &m)
}

type debugFlags struct {
	log     bool
	logFile string
	http    string
	dump    bool
}

func addDebugFlags(fs *flag.FlagSet) *debugFlags {
	d := &debugFlags{}
	fs.BoolVar(&d.log, "debug.log", false, "write a debug log, to a new file per run in $XDG_STATE_HOME/pipmix unless -debug.log-file is set")
	fs.StringVar(&d.logFile, "debug.log-file", "", "write the debug log, if -debug.log is set, to `file`, appending")
	fs.StringVar(&d.http, "debug.http", "", "serve Go's debug tools (/debug/pprof/) on `address`, such as localhost:6060")
	fs.BoolVar(&d.dump, "debug.dump", false, "print what PipeWire reports and exit")
	return d
}

// openDebugLog opens path for appending, or creates a new log file in the
// state directory.
func openDebugLog(path string) (*os.File, error) {
	if path != "" {
		return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "pipmix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("pipmix-%s-%d.log", time.Now().Format("20060102-150405"), os.Getpid())
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
}

func stateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

func quitKeys() section {
	return section{"Quit", []binding{
		{[]string{"q", "ctrl+c"}, "Quit", "", func(*mixer) command { return quit }},
	}}
}
