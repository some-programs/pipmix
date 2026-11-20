package tui

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Event is an input event from a [Terminal]: a [KeyEvent], [MouseEvent],
// [ResizeEvent], [FocusEvent] or [BlurEvent].
type Event any

// KeyEvent is a key press, named as in "a", "shift+tab", "ctrl+c", "pgup"
// or "space".
type KeyEvent string

// MouseKind is what a mouse did.
type MouseKind uint8

// The kinds of mouse events.
const (
	MouseClick MouseKind = iota
	MouseRelease
	MouseMotion
	MouseWheel
)

// Mouse buttons.
const (
	MouseNone      = ansi.MouseNone
	MouseLeft      = ansi.MouseLeft
	MouseMiddle    = ansi.MouseMiddle
	MouseRight     = ansi.MouseRight
	MouseWheelUp   = ansi.MouseWheelUp
	MouseWheelDown = ansi.MouseWheelDown
)

// MouseEvent is a mouse button press or release, motion while a button is
// held, or a wheel turn, at cell X, Y.
type MouseEvent struct {
	Kind   MouseKind
	X, Y   int
	Button ansi.MouseButton
}

// ResizeEvent reports the size of the terminal.
type ResizeEvent struct{ Width, Height int }

// FocusEvent reports that the terminal gained focus.
type FocusEvent struct{}

// BlurEvent reports that the terminal lost focus.
type BlurEvent struct{}

// convert returns the event for an ultraviolet event, or nil if pipmix has
// no use for it.
func convert(ev uv.Event) Event {
	mouse := func(kind MouseKind, m uv.Mouse) Event {
		return MouseEvent{Kind: kind, X: m.X, Y: m.Y, Button: m.Button}
	}
	switch ev := ev.(type) {
	case uv.KeyPressEvent:
		return KeyEvent(ev.String())
	case uv.MouseClickEvent:
		return mouse(MouseClick, uv.Mouse(ev))
	case uv.MouseReleaseEvent:
		return mouse(MouseRelease, uv.Mouse(ev))
	case uv.MouseMotionEvent:
		return mouse(MouseMotion, uv.Mouse(ev))
	case uv.MouseWheelEvent:
		return mouse(MouseWheel, uv.Mouse(ev))
	case uv.FocusEvent:
		return FocusEvent{}
	case uv.BlurEvent:
		return BlurEvent{}
	}
	return nil
}
