package pipmix

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/some-programs/pipmix/internal/tui"
)

// A message tells the mixer that something happened, such as a new
// PipeWire snapshot or an expired notice.
type message any

// A command does work, such as waiting for an update, away from the event
// loop, and returns the message to hand the mixer when done, or nil.
type command func() message

type quitMsg struct{}

// quit is the command that ends the event loop.
func quit() message { return quitMsg{} }

// after returns a command that returns msg after d.
func after(d time.Duration, msg message) command {
	return func() message {
		time.Sleep(d)
		return msg
	}
}

// terminalOptions returns the terminal modes the mixer needs.
func terminalOptions(o options) tui.Options {
	return tui.Options{Mouse: o.mouse, Focus: true}
}

// run shows m on the terminal until a quit key or a signal to stop.
func run(ctx context.Context, m *mixer) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	t, err := tui.Open(terminalOptions(m.opts))
	if err != nil {
		return err
	}
	defer t.Close()

	msgs := make(chan message, 16)
	exec := func(c command) {
		if c == nil {
			return
		}
		go func() {
			if msg := c(); msg != nil {
				select {
				case msgs <- msg:
				case <-ctx.Done():
				}
			}
		}()
	}
	for _, c := range m.init() {
		exec(c)
	}

	var (
		done   = ctx.Done()
		events = t.Events()
		grid   = tui.NewGrid(0, 0)
		screen = tui.NewScreen(t)
		frame  = time.Second / time.Duration(m.opts.fps)
		ticker = time.NewTicker(frame)
		timer  = time.NewTimer(frame)
		wake   = time.NewTimer(time.Hour)
		tickC  <-chan time.Time // the ticker's channel while the meters move
		frameC <-chan time.Time // the timer's channel while a frame waits
		sized  bool             // the size of the terminal is known
		full   bool             // the screen needs drawing
		meters bool             // the meters need drawing
		drawn  time.Time        // when the last frame was drawn
	)
	ticker.Stop()
	timer.Stop()
	wake.Stop()
	for {
		select {
		case <-done:
			return nil
		case ev := <-events:
			switch ev := ev.(type) {
			case tui.ResizeEvent:
				m.setSize(ev.Width, ev.Height)
				m.watchMeters()
				sized = true
				full = true
			case tui.KeyEvent:
				exec(m.handleKey(string(ev)))
				m.watchMeters()
				full = true
			case tui.MouseEvent:
				if ev.Kind == tui.MouseMotion {
					m.dragTo(ev)
				} else {
					exec(m.handleMouse(ev))
					m.watchMeters()
					full = true
				}
			default:
				exec(m.update(ev))
				full = true
			}
		case msg := <-msgs:
			switch msg.(type) {
			case quitMsg:
				return nil
			case meterMsg:
				// New levels redraw the meters, if they show.
				exec(m.update(msg))
				meters = meters || !m.metersOff()
			default:
				exec(m.update(msg))
				full = true
			}
		case <-tickC:
			exec(m.tickMetering())
			meters = true
		case <-frameC:
			frameC = nil
			m.flushDrag(false)
		case <-wake.C:
			m.refreshShown(time.Now())
		}

		if m.redraw {
			m.redraw = false
			full = true
		}
		// Set the wake timer to the time updateShown last asked for.
		if m.act.wakeAt.IsZero() {
			wake.Stop()
		} else {
			wake.Reset(time.Until(m.act.wakeAt))
		}

		if m.ticking != (tickC != nil) {
			if m.ticking {
				ticker.Reset(frame)
				tickC = ticker.C
			} else {
				ticker.Stop()
				tickC = nil
			}
		}
		if sized && (full || meters) && frameC == nil {
			if wait := frame - time.Since(drawn); wait > 0 {
				timer.Reset(wait)
				frameC = timer.C
			} else {
				if full {
					m.draw(grid)
				} else {
					m.drawMeters(grid)
				}
				full, meters = false, false
				drawn = time.Now()
				if err := screen.Flush(grid); err != nil {
					return err
				}
			}
		}
		if m.drag != nil && m.drag.last != m.drag.sent && frameC == nil {
			timer.Reset(max(m.dragInterval()-time.Since(m.drag.sentAt), time.Millisecond))
			frameC = timer.C
		}
	}
}
