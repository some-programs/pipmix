package pipmix

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/pw"
)

var (
	tabStyle        = lipgloss.NewStyle()
	activeTabStyle  = lipgloss.NewStyle().Reverse(true).Bold(true)
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	boldStyle       = lipgloss.NewStyle().Bold(true)
	focusStyle      = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	focusTitleStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	defaultTagStyle = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
	barFullStyle    = lipgloss.NewStyle().Foreground(lipgloss.Green)
	barOverStyle    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	menuCursorStyle = lipgloss.NewStyle().Reverse(true)
	menuBoxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

const (
	labelWidth = 5
	contentX   = 3
	barX       = 20
	listTop    = 1
)

func (m *mixer) barWidth() int { return m.width - barX - 2 }

func (m *mixer) innerWidth() int { return m.width - contentX - 2 }

func (m *mixer) listHeight() int {
	h := m.height - 1
	if m.statusLine() != "" {
		h--
	}
	return max(h, 0)
}

type lineKind int

const (
	lineBlank lineKind = iota
	lineTitle
	lineChannel
	lineNoVolume
	lineProfile
)

type itemLine struct {
	kind    lineKind
	channel int // -1 for folded channels
}

func (m *mixer) itemLines(it item) []itemLine {
	var lines []itemLine
	if it.groupCount <= 1 || it.groupLeader {
		lines = append(lines, itemLine{kind: lineTitle})
	}
	switch {
	case it.card != nil:
		lines = append(lines, itemLine{kind: lineProfile})
	case it.channels() == 0:
		lines = append(lines, itemLine{kind: lineNoVolume})
	case m.folded(it):
		lines = append(lines, itemLine{kind: lineChannel, channel: -1})
	default:
		for c := range it.channels() {
			lines = append(lines, itemLine{kind: lineChannel, channel: c})
		}
	}
	if it.card != nil {
		lines = append(lines, itemLine{kind: lineBlank})
	}
	return lines
}

func (m *mixer) itemHeight(it item) int { return len(m.itemLines(it)) }

// folded reports whether an item's channels show as one line: they are
// linked (locked) and show the same volume.
func (m *mixer) folded(it item) bool {
	if it.channels() < 2 || m.unlocked[lockKey(it)] {
		return false
	}
	first := percent(it.node.Channels[0].Volume)
	for _, c := range it.node.Channels[1:] {
		if percent(c.Volume) != first {
			return false
		}
	}
	return true
}

func percent(v float64) int { return int(math.Round(v * 100)) }

// foldedName returns the joined channel names (e.g. "FL+FR") if they fit in
// labelWidth, or else the channel count (e.g. "6ch").
func foldedName(chans []pw.Channel) string {
	var names []string
	for _, c := range chans {
		names = append(names, c.Name)
	}
	if s := strings.Join(names, "+"); len(s) <= labelWidth {
		return s
	}
	return fmt.Sprintf("%dch", len(chans))
}

// scroll adjusts the current tab's scroll offset so the focused item is visible.
func (m *mixer) scroll() {
	items := m.items(m.tab)
	i := m.focusIndex(m.tab, items)
	off := &m.offset[m.tab]
	if i < 0 {
		*off = 0
		return
	}
	top := 0
	for _, it := range items[:i] {
		top += m.itemHeight(it)
	}
	bottom := top + m.itemHeight(items[i])
	total := bottom
	for _, it := range items[i+1:] {
		total += m.itemHeight(it)
	}
	h := m.listHeight()
	if bottom > *off+h {
		*off = bottom - h
	}
	*off = max(min(*off, top, max(total-h, 0)), 0)
}

type tabSpan struct {
	tab        tab
	start, end int
}

const tabGap = 2

func tabSpans() []tabSpan {
	var spans []tabSpan
	x := 0
	for t := range tabCount {
		w := ansi.StringWidth(tabNames[t])
		spans = append(spans, tabSpan{t, x, x + w})
		x += w + tabGap
	}
	return spans
}

func (m *mixer) view() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	lines := []string{m.tabBar()}
	h := m.listHeight()
	var list []string
	if m.help {
		list = m.helpView(m.width, h)
	} else {
		items := m.items(m.tab)
		fi := m.focusIndex(m.tab, items)
		for i, it := range items {
			list = append(list, m.renderItem(it, i == fi)...)
		}
		if len(items) == 0 && m.snap.Connected {
			list = []string{dimStyle.Render(" Nothing here")}
		}
		off := min(m.offset[m.tab], max(len(list)-h, 0))
		list = list[off:]
	}
	list = list[:min(len(list), h)]
	for len(list) < h {
		list = append(list, "")
	}
	lines = append(lines, list...)
	if status := m.statusLine(); status != "" && m.height > 1 {
		lines = append(lines, fit(status, m.width))
	}
	content := strings.Join(lines, "\n")
	if m.menu != nil {
		box, x, y := m.menuBox()
		content = overlay(content, box, x, y)
	}
	return content
}

func (m *mixer) tabBar() string {
	var b strings.Builder
	for _, s := range tabSpans() {
		style := tabStyle
		if s.tab == m.tab {
			style = activeTabStyle
		}
		b.WriteString(style.Render(tabNames[s.tab]) + strings.Repeat(" ", tabGap))
	}
	return underlineRow(b.String(), m.width)
}

func underlineRow(s string, width int) string {
	const underline = "\x1b[4m"
	s = fit(s, width)
	for _, reset := range []string{"\x1b[m", "\x1b[0m"} {
		s = strings.ReplaceAll(s, reset, reset+underline)
	}
	return underline + s + ansi.ResetStyle
}

func (m *mixer) statusLine() string {
	switch {
	case !m.snap.Connected && m.snap.Err != nil:
		return errorStyle.Render(fmt.Sprintf("PipeWire: %v; reconnecting", m.snap.Err))
	case !m.snap.Connected:
		return "Connecting to PipeWire"
	case m.notice != "" && m.noticeErr:
		return errorStyle.Render(m.notice)
	case m.soloID != 0 && m.notice != "":
		return defaultTagStyle.Render("[SOLO] ") + m.soloTitle + "  " + m.notice
	case m.soloID != 0:
		return defaultTagStyle.Render("[SOLO] ") + m.soloTitle
	case m.notice != "":
		return m.notice
	}
	return ""
}

func (m *mixer) renderItem(it item, focused bool) []string {
	w := m.width
	if w < contentX+2 {
		return nil
	}
	inner := m.innerWidth()
	gutter := " " + typeStrip(it)
	line := func(s string) string { return gutter + " " + fit(s, inner) + "  " }
	var lines []string
	for _, l := range m.itemLines(it) {
		switch l.kind {
		case lineBlank:
			lines = append(lines, "")
		case lineTitle:
			groupFocused := focused || m.isGroupFocused(it)
			lines = append(lines, line(m.titleLine(it, groupFocused, inner)))
		case lineChannel:
			lines = append(lines, line(m.channelLine(it, l.channel, focused)))
		case lineNoVolume:
			if it.node.JACK {
				lines = append(lines, line(fixedBar(m.barWidth())))
				break
			}
			lines = append(lines, line(dimStyle.Render("volume not known yet")))
		case lineProfile:
			desc := "off"
			if it.card.Profile >= 0 {
				desc = it.card.Profiles[it.card.Profile].Description
			}
			lines = append(lines, line(dimStyle.Render("Profile: ")+desc))
		}
	}
	return lines
}

const typeStripBar = "█"

var (
	cardStripStyle   = lipgloss.NewStyle().Foreground(lipgloss.White)
	classStripStyles = [...]lipgloss.Style{
		pw.Playback:  lipgloss.NewStyle().Foreground(lipgloss.Green),
		pw.Recording: lipgloss.NewStyle().Foreground(lipgloss.Red),
		pw.Sink:      lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		pw.Source:    lipgloss.NewStyle().Foreground(lipgloss.Magenta),
	}
)

func typeStrip(it item) string {
	if it.node == nil {
		return cardStripStyle.Render(typeStripBar)
	}
	return classStripStyles[it.node.Class].Render(typeStripBar)
}

const (
	defaultTag  = "◉"
	minTitle    = 16
	minPortRoom = 4
)

func (m *mixer) tagsWidth() int {
	if m.tab == tabDefault || m.tab == tabOutputs || m.tab == tabInputs {
		return ansi.StringWidth(defaultTag)
	}
	return 0
}

// titleColumns returns the widths of the title and port columns for the
// current tab.
func (m *mixer) titleColumns(width int) (title, port int) {
	if m.tab == tabCards {
		return width, 0
	}
	for _, it := range m.items(m.tab) {
		port = max(port, ansi.StringWidth(portText(it)))
	}
	rest := width
	if tw := m.tagsWidth(); tw > 0 {
		rest -= tw + 1
	}
	port = min(port, rest-1-min(minTitle, rest/2))
	if port < minPortRoom {
		port = 0
	}
	if port > 0 {
		rest -= port + 1
	}
	return max(rest, 0), port
}

func portText(it item) string {
	if it.node == nil || len(it.node.Routes) == 0 {
		return ""
	}
	port := "no port"
	if r := it.node.Route; r >= 0 && r < len(it.node.Routes) {
		port = it.node.Routes[r].Description
	}
	if len(it.node.Routes) > 1 {
		port += " ▾"
	}
	return port
}

func tagsText(it item) string {
	if it.node.Default {
		return defaultTagStyle.Render(defaultTag)
	}
	return strings.Repeat(" ", ansi.StringWidth(defaultTag))
}

const toolbarWidth = barX - contentX

func (m *mixer) isGroupMuted(it item) bool {
	if it.groupCount <= 1 {
		return it.node != nil && it.node.Mute
	}
	for _, other := range m.items(m.tab) {
		if other.groupKey == it.groupKey && other.node != nil {
			if !other.node.Mute {
				return false
			}
		}
	}
	return true
}

func (m *mixer) isGroupFocused(it item) bool {
	if it.groupCount <= 1 || it.groupKey == "" {
		return false
	}
	items := m.items(m.tab)
	fi := m.focusIndex(m.tab, items)
	if fi < 0 || fi >= len(items) {
		return false
	}
	return items[fi].groupKey == it.groupKey
}

func (m *mixer) toolbar(it item, focused bool) string {
	if it.node == nil {
		return ""
	}
	var b strings.Builder
	muted := it.node.Mute
	if it.groupCount > 1 {
		muted = m.isGroupMuted(it)
	}
	b.WriteString(muteSymbol(muted, it.node.JACK, focused))
	if btn := playButton(it, focused); btn != "" {
		b.WriteByte(' ')
		b.WriteString(btn)
	}
	w := ansi.StringWidth(b.String())
	if w < toolbarWidth {
		b.WriteString(strings.Repeat(" ", toolbarWidth-w))
	}
	return b.String()
}

func (m *mixer) titleLine(it item, focused bool, width int) string {
	title := it.title()
	if it.groupCount > 1 && it.groupTitle != "" {
		title = it.groupTitle
	}
	if focused {
		title = focusTitleStyle.Render(title)
	}
	tb := m.toolbar(it, focused)
	avail := width
	if it.node != nil {
		avail = max(0, width-toolbarWidth)
	}
	if it.node == nil {
		return tb + title
	}
	tw := 0
	titleW := avail
	portW := 0
	if it.node.Class == pw.Sink || it.node.Class == pw.Source {
		tw = m.tagsWidth()
		if avail < tw+2 {
			return tb + title
		}
		titleW, portW = m.titleColumns(avail)
	}
	line := tb + fit(title, titleW)
	if portW > 0 {
		line += " " + dimStyle.Render(fit(portText(it), portW))
	}
	if tw > 0 {
		line += " " + tagsText(it)
	}
	return line
}

// portSpan returns the screen columns [start, end) of a device's port on
// its title line, if it shows one.
func (m *mixer) portSpan(it item) (start, end int, ok bool) {
	inner := m.innerWidth()
	text := portText(it)
	if text == "" || inner < toolbarWidth+m.tagsWidth()+2 {
		return 0, 0, false
	}
	avail := max(0, inner-toolbarWidth)
	titleW, portW := m.titleColumns(avail)
	if portW == 0 {
		return 0, 0, false
	}
	start = barX + titleW + 1
	return start, start + min(ansi.StringWidth(text), portW), true
}

const (
	symSpeaker = "🔊"
	symMuted   = "🔇"
	muteX      = contentX
	muteWidth  = 2
	symPlay    = "⏵"
	symPause   = "⏸"
	playX      = muteX + muteWidth + 1
	playWidth  = 1
)

var mutedButtonStyle = lipgloss.NewStyle().Foreground(lipgloss.Yellow)

func muteSymbol(muted, jack, focused bool) string {
	if muted && !jack {
		if focused {
			return mutedButtonStyle.Bold(true).Render(symMuted)
		}
		return mutedButtonStyle.Render(symMuted)
	}
	if focused {
		return focusStyle.Bold(true).Render(symSpeaker)
	}
	return symSpeaker
}

func muteButton(n *pw.Node, focused bool) string {
	return muteSymbol(n.Mute, n.JACK, focused)
}

func playButton(it item, focused bool) string {
	if it.player == nil {
		return ""
	}
	sym := symPlay
	if it.player.Playing {
		sym = symPause
	}
	if focused {
		return focusStyle.Bold(true).Render(sym)
	}
	return sym
}

func onMuteButton(it item, line itemLine, x int) bool {
	return it.node != nil && line.kind == lineTitle && x >= muteX && x < muteX+muteWidth
}

func onPlayButton(it item, line itemLine, x int) bool {
	return it.node != nil && it.player != nil && line.kind == lineTitle && x >= playX && x < playX+playWidth
}

const (
	streamMuteX     = contentX + 2
	streamMuteWidth = 2
)

func onStreamMuteButton(it item, line itemLine, x int) bool {
	return it.groupCount > 1 && it.node != nil && line.kind == lineChannel &&
		(line.channel == -1 || line.channel == 0) &&
		x >= streamMuteX && x < streamMuteX+streamMuteWidth
}

// channelLine renders a channel row (or folded row when i is -1).
func (m *mixer) channelLine(it item, i int, focused bool) string {
	n := it.node
	unlocked := m.isUnlocked(it)
	chanFocused := focused && (!unlocked || m.focus[m.tab].channel == i)

	var c pw.Channel
	var nameStr string
	if i < 0 && len(n.Channels) > 1 {
		c = n.Channels[0]
		nameStr = foldedName(n.Channels)
	} else {
		c = n.Channels[max(i, 0)]
		nameStr = ansi.Truncate(c.Name, labelWidth, "…")
	}
	name := strings.Repeat(" ", max(0, labelWidth-ansi.StringWidth(nameStr))) + nameStr
	vol := "   "
	if !n.JACK {
		vol = fmt.Sprintf("%3d", percent(c.Volume))
	}
	if chanFocused {
		name = boldStyle.Render(name)
		vol = boldStyle.Render(vol)
	}

	var prefix string
	if it.groupCount > 1 {
		if i < 0 || i == 0 {
			branch := "├"
			if it.groupIndex == 0 {
				branch = "┌"
			} else if it.groupLast {
				branch = "└"
			}
			branchStr := dimStyle.Render(branch) + " "
			spkStr := muteButton(n, focused) + " "
			innerBracket := "  "
			if len(n.Channels) > 1 && i >= 0 {
				innerBracket = dimStyle.Render("┌") + " "
			}
			prefix = branchStr + spkStr + innerBracket
		} else {
			stem := "  "
			if !it.groupLast {
				stem = dimStyle.Render("│") + " "
			}
			spkSpace := "   "
			innerBracket := dimStyle.Render("├") + " "
			if i == len(n.Channels)-1 {
				innerBracket = dimStyle.Render("└") + " "
			}
			prefix = stem + spkSpace + innerBracket
		}
	} else {
		if i < 0 || len(n.Channels) <= 1 {
			prefix = "       "
		} else {
			bracket := "├"
			if i == 0 {
				bracket = "┌"
			} else if i == len(n.Channels)-1 {
				bracket = "└"
			}
			prefix = "     " + dimStyle.Render(bracket) + " "
		}
	}

	label := prefix + name + " " + vol + " "
	if n.JACK {
		return label + meterBar(m.level(it, i), m.peak(it, i), m.barWidth(), m.opts.limit, m.look())
	}
	return label + bar(c.Volume, m.level(it, i), m.peak(it, i), m.barWidth(), m.opts.limit, n.Mute, m.look())
}

const (
	faderFull  = "▓"
	faderKnob  = "█"
	faderEmpty = "░"
)

func fixedBar(width int) string {
	return strings.Repeat(" ", barX-contentX) + dimStyle.Render(strings.Repeat(faderEmpty, max(width, 0)))
}

// meterBar renders a level meter of width cells without a volume fader.
func meterBar(level, peak float64, width int, limit float64, look meterLook) string {
	if width <= 0 {
		return ""
	}
	lit, pk := 0, 0
	if look != meterOff {
		if level > 0 {
			lit = knobCell(level, limit, width)
		}
		if peak > 0 {
			pk = knobCell(peak, limit, width)
		}
	}
	if lit == 0 && pk == 0 {
		return dimStyle.Render(strings.Repeat(faderEmpty, width))
	}
	hundred := knobCell(1, limit, width)
	styles := [...]lipgloss.Style{barFullStyle, barOverStyle, brightFull, brightOver, dimStyle}
	type cell struct {
		glyph string
		style int
	}
	cells := make([]cell, width)
	for i := range cells {
		over := 0
		if i > hundred {
			over = 1
		}
		c := cell{faderEmpty, 4}
		switch {
		case i < lit && (look == meterSolid || look == meterShade):
			c = cell{faderKnob, over}
		case i == lit-1 && look == meterDot:
			c = cell{faderKnob, 2 + over}
		}
		if i == pk-1 {
			c = cell{faderKnob, 2 + over}
		}
		cells[i] = c
	}
	var b strings.Builder
	for i := 0; i < len(cells); {
		j := i + 1
		for j < len(cells) && cells[j] == cells[i] {
			j++
		}
		b.WriteString(styles[cells[i].style].Render(strings.Repeat(cells[i].glyph, j-i)))
		i = j
	}
	return b.String()
}

var (
	emptyOverStyle = lipgloss.NewStyle().Foreground(lipgloss.Red).Faint(true)
	brightFull     = lipgloss.NewStyle().Foreground(lipgloss.BrightGreen)
	brightOver     = lipgloss.NewStyle().Foreground(lipgloss.BrightRed)
)

// bar renders a fader of width cells for volume v, level, and peak on [0, limit].
func bar(v, level, peak float64, width int, limit float64, muted bool, look meterLook) string {
	if width <= 0 {
		return ""
	}
	knob := knobCell(v, limit, width)
	hundred := knobCell(1, limit, width)
	var b strings.Builder
	run := func(s string, from, to int, style lipgloss.Style) {
		if to > from {
			b.WriteString(style.Render(strings.Repeat(s, to-from)))
		}
	}
	if muted {
		run(faderFull, 0, knob, dimStyle)
		run(faderKnob, knob, knob+1, dimStyle)
		run(faderEmpty, knob+1, width, dimStyle)
		return b.String()
	}
	knobStyle := barFullStyle
	if knob > hundred {
		knobStyle = barOverStyle
	}
	styles := [...]lipgloss.Style{barFullStyle, barOverStyle, brightFull, brightOver}
	type cell struct {
		glyph string
		style int
	}
	lit, pk := 0, 0
	if look != meterOff {
		if level > 0 {
			lit = min(knobCell(level, limit, width), knob)
		}
		if peak > 0 {
			pk = min(knobCell(peak, limit, width), knob)
		}
	}
	cells := make([]cell, knob)
	for i := range cells {
		over := 0
		if i > hundred {
			over = 1
		}
		c := cell{faderFull, over}
		switch {
		case i < lit && (look == meterSolid || look == meterShade):
			c.glyph = faderKnob
		case i == lit-1 && look == meterDot:
			c = cell{faderKnob, 2 + over}
		case look == meterShade:
			c.glyph = "▒"
		}
		if i == pk-1 {
			c = cell{faderKnob, 2 + over}
		}
		cells[i] = c
	}
	for i := 0; i < len(cells); {
		j := i + 1
		for j < len(cells) && cells[j] == cells[i] {
			j++
		}
		run(cells[i].glyph, i, j, styles[cells[i].style])
		i = j
	}
	run(faderKnob, knob, knob+1, knobStyle)
	run(faderEmpty, knob+1, max(knob+1, hundred+1), dimStyle)
	run(faderEmpty, max(knob+1, hundred+1), width, emptyOverStyle)
	return b.String()
}

func knobCell(v, limit float64, width int) int {
	k := int(math.Round(v / limit * float64(width-1)))
	return max(0, min(k, width-1))
}

func (m *mixer) menuRows() (first, n int) {
	mn := m.menu
	n = max(min(len(mn.items), m.listHeight()-3), 1)
	first = max(0, min(mn.cursor-n/2, len(mn.items)-n))
	return first, n
}

func (m *mixer) menuBox() (box string, x, y int) {
	mn := m.menu
	width := ansi.StringWidth(mn.title)
	for _, it := range mn.items {
		width = max(width, ansi.StringWidth(it.label)+2)
	}
	width = max(min(width, m.width-4), 1)
	first, n := m.menuRows()
	lines := []string{boldStyle.Render(ansi.Truncate(mn.title, width, "…"))}
	for i := first; i < min(first+n, len(mn.items)); i++ {
		it := mn.items[i]
		mark := "  "
		if it.active {
			mark = "● "
		}
		label := fit(mark+it.label, width)
		switch {
		case i == mn.cursor:
			label = menuCursorStyle.Render(label)
		case it.dim:
			label = dimStyle.Render(label)
		}
		lines = append(lines, label)
	}
	box = menuBoxStyle.Render(strings.Join(lines, "\n"))
	bw := ansi.StringWidth(strings.SplitN(box, "\n", 2)[0])
	bh := strings.Count(box, "\n") + 1
	return box, max((m.width-bw)/2, 0), listTop + max(m.listHeight()-bh, 0)/2
}

// overlay draws box over content with its top left corner at cell x, y.
func overlay(content, box string, x, y int) string {
	lines := strings.Split(content, "\n")
	x = max(0, x)
	for i, b := range strings.Split(box, "\n") {
		if y+i >= len(lines) {
			break
		}
		line := lines[y+i]
		left := ansi.Truncate(line, x, "")
		left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
		right := ansi.Cut(line, x+ansi.StringWidth(b), ansi.StringWidth(line))
		lines[y+i] = left + ansi.ResetStyle + b + ansi.ResetStyle + right
	}
	return strings.Join(lines, "\n")
}
