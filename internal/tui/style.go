// Package tui draws text screens into cell grids and shows them on a
// terminal, writing only the cells that changed.
package tui

import "strconv"

// Color is one of the 16 basic terminal colors, or the terminal's default.
type Color uint8

// The basic colors. Their order follows the ANSI color numbers.
const (
	Default Color = iota
	Black
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	White
	BrightBlack
	BrightRed
	BrightGreen
	BrightYellow
	BrightBlue
	BrightMagenta
	BrightCyan
	BrightWhite
)

// Attr is a set of text attributes.
type Attr uint8

// The text attributes.
const (
	Bold Attr = 1 << iota
	Faint
	Underline
	Reverse
)

// Style is how a cell looks. The zero Style is the terminal's default.
type Style struct {
	Fg             Color
	Attrs          Attr
	UnderlineColor Color
}

// appendSGR appends the escape sequence that sets the terminal to s from
// any style.
func appendSGR(b []byte, s Style) []byte {
	b = append(b, "\x1b[0"...)
	for _, a := range [...]struct {
		attr Attr
		code string
	}{{Bold, ";1"}, {Faint, ";2"}, {Underline, ";4"}, {Reverse, ";7"}} {
		if s.Attrs&a.attr != 0 {
			b = append(b, a.code...)
		}
	}
	switch c := int(s.Fg) - 1; {
	case c < 0:
	case c < 8:
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(30+c), 10)
	default:
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(90+c-8), 10)
	}
	if s.UnderlineColor != Default {
		b = append(b, ";58;5;"...)
		b = strconv.AppendInt(b, int64(s.UnderlineColor-1), 10)
	}
	return append(b, 'm')
}
