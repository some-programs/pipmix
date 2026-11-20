package tui

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
)

// Options are the terminal modes a [Terminal] sets.
type Options struct {
	Mouse bool // report mouse buttons, wheel and motion while a button is held
	Focus bool // report focus and blur
}

// Terminal is the controlling terminal in raw mode, showing the alternate
// screen. Its input arrives as events.
type Terminal struct {
	in, out *os.File
	state   *term.State
	opts    Options
	reader  cancelreader.CancelReader
	cancel  context.CancelFunc
	events  chan Event
	winch   chan os.Signal
	done    chan struct{} // closed when the input goroutine has returned
	close   sync.Once
}

// Open sets up the terminal on standard input and output and starts
// reading its input. The first event is a [ResizeEvent]. Call
// [Terminal.Close] to restore the terminal.
func Open(opts Options) (*Terminal, error) {
	in, out := os.Stdin, os.Stdout
	if !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return nil, errors.New("standard input and output must be a terminal")
	}
	state, err := term.MakeRaw(in.Fd())
	if err != nil {
		return nil, err
	}
	setup := "\x1b[?1049h\x1b[?25l\x1b[?2004h"
	if opts.Focus {
		setup += "\x1b[?1004h"
	}
	if opts.Mouse {
		setup += "\x1b[?1002h\x1b[?1006h"
	}
	if _, err := out.WriteString(setup); err != nil {
		_ = term.Restore(in.Fd(), state)
		return nil, err
	}
	reader, err := uv.NewCancelReader(in)
	if err != nil {
		_, _ = out.WriteString(teardown(opts))
		_ = term.Restore(in.Fd(), state)
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Terminal{
		in: in, out: out, state: state, opts: opts, reader: reader, cancel: cancel,
		events: make(chan Event, 64),
		winch:  make(chan os.Signal, 1),
		done:   make(chan struct{}),
	}
	if w, h, err := term.GetSize(out.Fd()); err == nil {
		t.events <- ResizeEvent{w, h}
	}
	signal.Notify(t.winch, syscall.SIGWINCH)
	go t.watchSize(ctx)

	raw := make(chan uv.Event)
	go func() {
		defer close(t.done)
		_ = uv.NewTerminalReader(reader, os.Getenv("TERM")).StreamEvents(ctx, raw)
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-raw:
				if e := convert(ev); e != nil {
					t.send(ctx, e)
				}
			}
		}
	}()
	return t, nil
}

func (t *Terminal) send(ctx context.Context, e Event) {
	select {
	case t.events <- e:
	case <-ctx.Done():
	}
}

func (t *Terminal) watchSize(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.winch:
			if w, h, err := term.GetSize(t.out.Fd()); err == nil {
				t.send(ctx, ResizeEvent{w, h})
			}
		}
	}
}

// Events returns the channel of input events.
func (t *Terminal) Events() <-chan Event { return t.events }

// Write writes p to the terminal.
func (t *Terminal) Write(p []byte) (int, error) { return t.out.Write(p) }

// Close stops reading input and restores the terminal as Open found it.
func (t *Terminal) Close() error {
	var err error
	t.close.Do(func() {
		signal.Stop(t.winch)
		t.reader.Cancel()
		t.cancel()
		select {
		case <-t.done:
		case <-time.After(500 * time.Millisecond):
			// The read did not stop; give up on it.
		}
		_ = t.reader.Close()
		_, err = t.out.WriteString(teardown(t.opts))
		err = errors.Join(err, term.Restore(t.in.Fd(), t.state))
	})
	return err
}

// teardown returns what undoes the modes Open sets.
func teardown(opts Options) string {
	s := "\x1b[0m"
	if opts.Mouse {
		s += "\x1b[?1006l\x1b[?1002l"
	}
	if opts.Focus {
		s += "\x1b[?1004l"
	}
	return s + "\x1b[?2004l\x1b[?25h\x1b[?1049l"
}
