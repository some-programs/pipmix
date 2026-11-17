// Package pw is a client for PipeWire's native protocol.
package pw

import (
	"context"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"
)

const (
	minBackoff = 250 * time.Millisecond
	maxBackoff = 3 * time.Second
)

// A Client is a connection to PipeWire that reconnects when lost.
type Client struct {
	// PID is the process ID used to identify this instance's meter streams.
	// Defaults to os.Getpid() if zero.
	PID int

	log     *log.Logger
	cmds    chan func(*session)
	snap    atomic.Pointer[Snapshot]
	updated chan struct{}
}

// NewClient returns a client that logs to logger.
func NewClient(logger *log.Logger) *Client {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	c := &Client{
		PID:     os.Getpid(),
		log:     logger,
		cmds:    make(chan func(*session), 256),
		updated: make(chan struct{}, 1),
	}
	c.snap.Store(&Snapshot{})
	return c
}

// Snapshot returns the latest state.
func (c *Client) Snapshot() *Snapshot { return c.snap.Load() }

// Updated returns a channel that receives when a new snapshot is out.
// Updates in quick succession may arrive as one.
func (c *Client) Updated() <-chan struct{} { return c.updated }

func (c *Client) publish(s *Snapshot) {
	c.snap.Store(s)
	select {
	case c.updated <- struct{}{}:
	default:
	}
}

// do queues a command for the connection's loop. Commands are dropped when
// the queue is full or the connection is lost before they run.
func (c *Client) do(f func(*session)) {
	select {
	case c.cmds <- f:
	default:
		c.log.Print("command queue full, dropping a command")
	}
}

// ChangeVolume steps the volume of node's channel (or all channels if channel
// is negative) by delta on the cubic scale, clamped to [0, limit].
func (c *Client) ChangeVolume(node uint32, channel int, delta, limit float64) {
	c.do(func(s *session) { s.changeVolume(node, channel, delta, limit) })
}

// SetVolume sets the volume of node's channel, or of all its channels if
// channel is negative, to v, within [0, limit].
func (c *Client) SetVolume(node uint32, channel int, v, limit float64) {
	c.do(func(s *session) { s.setVolume(node, channel, v, limit) })
}

// ToggleMute mutes or unmutes node.
func (c *Client) ToggleMute(node uint32) {
	c.do(func(s *session) { s.toggleMute(node) })
}

// SetMute sets the mute state of node.
func (c *Client) SetMute(node uint32, mute bool) {
	c.do(func(s *session) { s.setMute(node, mute) })
}

// SetRoute switches a device node to the route with index, a Route.Index.
func (c *Client) SetRoute(node uint32, index int32) {
	c.do(func(s *session) { s.setRoute(node, index) })
}

// SetProfile switches card to the profile with index, a Profile.Index.
func (c *Client) SetProfile(card uint32, index int32) {
	c.do(func(s *session) { s.setProfile(card, index) })
}

// SetDefault makes a sink or source node the default one.
func (c *Client) SetDefault(node uint32) {
	c.do(func(s *session) { s.setDefault(node) })
}

// JackMeterReady unlinks a JACK meter stream from its initial target once
// pipewire-pulse has created the stream, leaving only the JACK node's links.
func (c *Client) JackMeterReady(ctx context.Context, name string) {
	done := make(chan struct{})
	c.do(func(s *session) { s.jackMeterReady(name, done) })
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}

// Run connects and keeps the connection up until ctx is done.
func (c *Client) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.log.Printf("pipewire: connection lost: %v", err)
		c.publish(&Snapshot{Err: err})
		if time.Since(start) > 10*time.Second {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// session runs one connection until it fails or ctx is done.
func (c *Client) session(ctx context.Context) error {
	conn, err := dial()
	if err != nil {
		return err
	}
	defer conn.close()
	// Commands queued while disconnected refer to the old connection's
	// objects, whose ids may now mean something else.
	for len(c.cmds) > 0 {
		<-c.cmds
	}

	msgs := make(chan message, 64)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			m, err := conn.read()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case msgs <- m:
			case <-done:
				return
			}
		}
	}()

	s := newSession(conn, c.log, c.PID)
	s.start()
	var expiry *time.Timer
	for s.err == nil {
		var expired <-chan time.Time
		if t := s.nextExpiry(time.Now()); !t.IsZero() {
			if expiry == nil {
				expiry = time.NewTimer(time.Until(t))
			} else {
				expiry.Reset(time.Until(t))
			}
			expired = expiry.C
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case m := <-msgs:
			for {
				if err := s.handle(m); err != nil {
					c.log.Print(err)
				}
				if len(msgs) == 0 || s.err != nil {
					break
				}
				m = <-msgs
			}
		case f := <-c.cmds:
			f(s)
		case <-expired:
			s.dirty = true
		}
		if s.ready && s.dirty {
			s.dirty = false
			c.publish(s.snapshot(time.Now()))
		}
	}
	return s.err
}
