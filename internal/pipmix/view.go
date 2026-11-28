package pipmix

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/pw"
	"github.com/some-programs/pipmix/internal/tui"
)

var (
	plain           = tui.Style{}
	tabStyle        = tui.Style{}
	activeTabStyle  = tui.Style{Attrs: tui.Reverse | tui.Bold}
	dimStyle        = tui.Style{Fg: tui.BrightBlack}
	boldStyle       = tui.Style{Attrs: tui.Bold}
	focusStyle      = tui.Style{Fg: tui.Cyan}
	focusTitleStyle = tui.Style{Attrs: tui.Bold | tui.Underline}
	defaultTagStyle = tui.Style{Fg: tui.Cyan}
	errorStyle      = tui.Style{Fg: tui.Red, Attrs: tui.Bold}
	barFullStyle    = tui.Style{Fg: tui.Green}
	barOverStyle    = tui.Style{Fg: tui.Red}
	menuCursorStyle = tui.Style{Attrs: tui.Reverse}
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
	if m.status() != nil {
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

func (m *mixer) itemHeight(it item) int {
	n := 0
	if it.groupCount <= 1 || it.groupLeader {
		n++
	}
	switch {
	case it.card != nil:
		n += 2
	case it.channels() == 0, m.folded(it):
		n++
	default:
		n += it.channels()
	}
	return n
}

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
func (m *mixer) scroll() { m.scrollItems(m.items(m.tab)) }

func (m *mixer) scrollItems(items []item) {
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

var cachedTabSpans = func() [tabCount]tabSpan {
	var spans [tabCount]tabSpan
	x := 0
	for t := range tabCount {
		w := ansi.StringWidth(tabNames[t])
		spans[t] = tabSpan{t, x, x + w}
		x += w + tabGap
	}
	return spans
}()

func tabSpans() []tabSpan { return cachedTabSpans[:] }

// meterRow is a row of the screen that shows a meter.
type meterRow struct {
	y       int
	it      item
	channel int
}

// draw draws the whole screen into g, resizing it to the mixer, and notes
// the rows that show meters for [mixer.drawMeters].
func (m *mixer) draw(g *tui.Grid) {
	if w, h := g.Size(); w != m.width || h != m.height {
		g.Resize(m.width, m.height)
	} else {
		g.Clear()
	}
	m.meterRows = m.meterRows[:0]
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.drawTabBar(g)
	h := m.listHeight()
	if m.help {
		m.drawHelp(g, h)
	} else {
		m.drawList(g, h)
	}
	if status := m.status(); status != nil && m.height > 1 {
		p := g.Pen(0, listTop+h, m.width)
		for _, s := range status {
			p.Text(s.text, s.style)
		}
	}
	if m.menu != nil {
		m.drawMenu(g)
	}
}

// drawMeters redraws the meters that the last [mixer.draw] drew, which
// must be the only change since.
func (m *mixer) drawMeters(g *tui.Grid) {
	if m.menu != nil {
		// The menu may cover meters.
		m.draw(g)
		return
	}
	w := m.barWidth()
	for _, r := range m.meterRows {
		p := g.Pen(barX, r.y, barX+w)
		m.drawChannelBar(&p, r.it, r.channel)
	}
}

// view draws the screen and returns it as lines.
func (m *mixer) view() string {
	g := tui.NewGrid(m.width, m.height)
	m.draw(g)
	return g.String()
}

func (m *mixer) drawTabBar(g *tui.Grid) {
	p := g.Pen(0, 0, m.width)
	p.AddAttrs(tui.Underline)
	for _, s := range tabSpans() {
		style := tabStyle
		if s.tab == m.tab {
			style = activeTabStyle
		}
		p.Text(tabNames[s.tab], style)
		p.Repeat(" ", tabGap, plain)
	}
	p.Pad(plain)
}

func (m *mixer) drawList(g *tui.Grid, h int) {
	layout := m.tabLayout(m.tab)
	if len(layout.items) == 0 {
		if m.snap.Connected && h > 0 {
			p := g.Pen(0, listTop, m.width)
			p.Text(" Nothing here", dimStyle)
		}
		return
	}
	if m.width < contentX+2 {
		return
	}
	lines := make([][]itemLine, len(layout.items))
	total := 0
	for i, it := range layout.items {
		lines[i] = m.itemLines(it)
		total += len(lines[i])
	}
	y := listTop - min(m.offset[m.tab], max(total-h, 0))
	for i, it := range layout.items {
		for _, l := range lines[i] {
			if y >= listTop+h {
				return
			}
			if y >= listTop {
				m.drawLine(g, y, it, l, i == layout.focus, layout)
			}
			y++
		}
	}
}

type span struct {
	text  string
	style tui.Style
}

// status returns the status line, or nil if there is none.
func (m *mixer) status() []span {
	switch {
	case !m.snap.Connected && m.snap.Err != nil:
		return []span{{fmt.Sprintf("PipeWire: %v; reconnecting", m.snap.Err), errorStyle}}
	case !m.snap.Connected:
		return []span{{"Connecting to PipeWire", plain}}
	case m.notice != "" && m.noticeErr:
		return []span{{m.notice, errorStyle}}
	case m.soloID != 0 && m.notice != "":
		return []span{{"[SOLO] ", defaultTagStyle}, {m.soloTitle + "  " + m.notice, plain}}
	case m.soloID != 0:
		return []span{{"[SOLO] ", defaultTagStyle}, {m.soloTitle, plain}}
	case m.notice != "":
		return []span{{m.notice, plain}}
	}
	return nil
}

// statusLine returns the text of the status line.
func (m *mixer) statusLine() string {
	var b strings.Builder
	for _, s := range m.status() {
		b.WriteString(s.text)
	}
	return b.String()
}

type tabLayout struct {
	items     []item
	focus     int
	portWidth int
	outputs   bool // playback streams play to more than one node
	short     bool // stream labels use short device names
}

func (m *mixer) tabLayout(t tab) tabLayout {
	items := m.items(t)
	l := tabLayout{
		items:     items,
		focus:     m.focusIndex(t, items),
		portWidth: m.portWidth(items),
		outputs:   playbackOutputs(m.snap) > 1,
	}
	// If one stream's label doesn't fit beside its title, all are short.
	room := m.labelRoom()
	for _, it := range items {
		if label := m.streamLabel(it, l.outputs, false); label != "" &&
			ansi.StringWidth(shownTitle(it))+1+ansi.StringWidth(label) > room {
			l.short = true
			break
		}
	}
	return l
}

// shownTitle returns the title drawn on the title line of it: its group's,
// if grouped.
func shownTitle(it item) string {
	if it.groupCount > 1 && it.groupTitle != "" {
		return it.groupTitle
	}
	return it.title()
}

// labelRoom returns the width that a stream's title and label share.
func (m *mixer) labelRoom() int {
	room := max(0, m.innerWidth()-toolbarWidth)
	if t := m.tagsWidth(); t > 0 {
		room -= t + 1
	}
	return room
}

func (l tabLayout) isGroupMuted(it item) bool {
	if it.groupCount <= 1 {
		return it.node != nil && it.node.Mute
	}
	for _, other := range l.items {
		if other.groupKey == it.groupKey && other.node != nil {
			if !other.node.Mute {
				return false
			}
		}
	}
	return true
}

func (l tabLayout) isGroupFocused(it item) bool {
	if it.groupCount <= 1 || it.groupKey == "" || l.focus < 0 || l.focus >= len(l.items) {
		return false
	}
	return l.items[l.focus].groupKey == it.groupKey
}

// drawLine draws line l of item it on row y.
func (m *mixer) drawLine(g *tui.Grid, y int, it item, l itemLine, focused bool, layout tabLayout) {
	if l.kind == lineBlank {
		return
	}
	p := g.Pen(0, y, m.width)
	p.Text(" ", plain)
	p.Text(typeStripBar, typeStripStyle(it))
	p.Text(" ", plain)
	c := p.Sub(m.innerWidth())
	switch l.kind {
	case lineTitle:
		m.drawTitle(&c, it, focused || layout.isGroupFocused(it), layout)
	case lineChannel:
		m.drawChannel(&c, y, it, l.channel, focused)
	case lineNoVolume:
		if it.node.JACK {
			c.Repeat(" ", barX-contentX, plain)
			c.Repeat(faderEmpty, m.barWidth(), dimStyle)
			break
		}
		c.Text("volume not known yet", dimStyle)
	case lineProfile:
		desc := "off"
		if it.card.Profile >= 0 {
			desc = it.card.Profiles[it.card.Profile].Description
		}
		c.Text("Profile: ", dimStyle)
		c.Text(desc, plain)
	}
	c.Pad(plain)
	p.Join(c)
}

const typeStripBar = "█"

var (
	cardStripStyle   = tui.Style{Fg: tui.White}
	classStripStyles = [...]tui.Style{
		pw.Playback:  {Fg: tui.Green},
		pw.Recording: {Fg: tui.Red},
		pw.Sink:      {Fg: tui.Yellow},
		pw.Source:    {Fg: tui.Magenta},
	}
)

func typeStripStyle(it item) tui.Style {
	if it.node == nil {
		return cardStripStyle
	}
	return classStripStyles[it.node.Class]
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

func (m *mixer) portWidth(items []item) int {
	if m.tab == tabCards {
		return 0
	}
	port := 0
	for _, it := range items {
		port = max(port, ansi.StringWidth(portText(it)))
	}
	return port
}

// titleColumns returns the widths of the title and port columns for the
// current tab.
func (m *mixer) titleColumns(width, portW int) (title, port int) {
	if m.tab == tabCards {
		return width, 0
	}
	rest := width
	if tw := m.tagsWidth(); tw > 0 {
		rest -= tw + 1
	}
	port = min(portW, rest-1-min(minTitle, rest/2))
	if port < minPortRoom {
		port = 0
	}
	if port > 0 {
		rest -= port + 1
	}
	return max(rest, 0), port
}

// playbackOutputs returns how many nodes playback streams play to.
func playbackOutputs(s *pw.Snapshot) int {
	var seen []uint32
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.Class != pw.Playback || n.JACK {
			continue
		}
		for _, id := range n.To {
			if !slices.Contains(seen, id) {
				seen = append(seen, id)
			}
		}
	}
	return len(seen)
}

// streamLabel returns what stream it records from or, if outputs is set,
// plays to, as a label for its title line. short shortens device names.
func (m *mixer) streamLabel(it item, outputs, short bool) string {
	n := it.node
	if n == nil || n.JACK {
		return ""
	}
	var ids []uint32
	var many string
	switch {
	case n.Class == pw.Recording:
		ids, many = n.From, "sources"
	case n.Class == pw.Playback && outputs:
		ids, many = n.To, "outputs"
	default:
		return ""
	}
	switch len(ids) {
	case 0:
		return ""
	case 1:
	default:
		return strconv.Itoa(len(ids)) + " " + many
	}
	other := snapNode(m.snap, ids[0])
	if other == nil {
		return ""
	}
	name := other.Title
	if short {
		name = shortDeviceName(name)
	}
	if n.Class == pw.Recording && other.Class == pw.Sink {
		return "monitor of " + name
	}
	return name
}

// profileSuffix matches the profile PipeWire appends to device names, such
// as " Analog Stereo" or " Mono", before any parenthesized detail.
var profileSuffix = regexp.MustCompile(`(?:\s+(?:Analog|Digital|Pro))?\s+(?:Stereo|Mono|Surround \d+(?:\.\d+)?)(\s+\(.*\))?$`)

// shortDeviceName returns device name without the profile PipeWire appends.
func shortDeviceName(name string) string {
	if short := profileSuffix.ReplaceAllString(name, "$1"); short != "" {
		return short
	}
	return name
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

const toolbarWidth = barX - contentX

func (m *mixer) drawToolbar(p *tui.Pen, it item, focused bool, layout tabLayout) {
	c := p.Sub(toolbarWidth)
	muted := it.node.Mute
	if it.groupCount > 1 {
		muted = layout.isGroupMuted(it)
	}
	c.Text(muteSymbol(muted, it.node.JACK, focused))
	if btn, st := playButton(it, focused); btn != "" {
		c.Text(" ", plain)
		c.Text(btn, st)
	}
	c.Pad(plain)
	p.Join(c)
}

// drawTitle draws the title line of it into p, which spans the inner width.
func (m *mixer) drawTitle(p *tui.Pen, it item, focused bool, layout tabLayout) {
	title := shownTitle(it)
	titleStyle := plain
	if focused {
		titleStyle = focusTitleStyle
	}
	if it.node == nil {
		p.Text(title, titleStyle)
		return
	}
	m.drawToolbar(p, it, focused, layout)
	avail := max(0, m.innerWidth()-toolbarWidth)
	tw := 0
	titleW := avail
	portW := 0
	if it.node.Class == pw.Sink || it.node.Class == pw.Source {
		tw = m.tagsWidth()
		if avail < tw+2 {
			p.Text(title, titleStyle)
			return
		}
		titleW, portW = m.titleColumns(avail, layout.portWidth)
	} else if label := m.streamLabel(it, layout.outputs, layout.short); label != "" {
		// Streams show what they play to or record from, at the right, up
		// to the tag column of devices.
		room := m.labelRoom()
		lw := min(ansi.StringWidth(label), room-1-min(minTitle, room/2))
		if lw >= minPortRoom {
			p.Fit(title, room-lw-1, titleStyle, plain)
			p.Text(" ", plain)
			p.Fit(label, lw, dimStyle, dimStyle)
			p.Pad(plain)
			return
		}
	}
	p.Fit(title, titleW, titleStyle, plain)
	if portW > 0 {
		p.Text(" ", plain)
		p.Fit(portText(it), portW, dimStyle, dimStyle)
	}
	if tw > 0 {
		p.Text(" ", plain)
		if it.node.Default {
			p.Text(defaultTag, defaultTagStyle)
		} else {
			p.Repeat(" ", tw, plain)
		}
	}
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
	titleW, portW := m.titleColumns(avail, m.portWidth(m.items(m.tab)))
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

var mutedButtonStyle = tui.Style{Fg: tui.Yellow}

func muteSymbol(muted, jack, focused bool) (string, tui.Style) {
	if muted && !jack {
		if focused {
			return symMuted, tui.Style{Fg: mutedButtonStyle.Fg, Attrs: tui.Bold}
		}
		return symMuted, mutedButtonStyle
	}
	if focused {
		return symSpeaker, tui.Style{Fg: focusStyle.Fg, Attrs: tui.Bold}
	}
	return symSpeaker, plain
}

func playButton(it item, focused bool) (string, tui.Style) {
	if it.player == nil {
		return "", plain
	}
	sym := symPlay
	if it.player.Playing {
		sym = symPause
	}
	if focused {
		return sym, tui.Style{Fg: focusStyle.Fg, Attrs: tui.Bold}
	}
	return sym, plain
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

// drawChannel draws channel i of it (or its folded channels when i is -1)
// into p, which spans the inner width of row y.
func (m *mixer) drawChannel(p *tui.Pen, y int, it item, i int, focused bool) {
	n := it.node
	unlocked := m.isUnlocked(it)
	chanFocused := focused && (!unlocked || m.focus[m.tab].channel == i)

	var c pw.Channel
	var name string
	if i < 0 && len(n.Channels) > 1 {
		c = n.Channels[0]
		name = foldedName(n.Channels)
	} else {
		c = n.Channels[max(i, 0)]
		name = ansi.Truncate(c.Name, labelWidth, "…")
	}
	labelStyle := plain
	if chanFocused {
		labelStyle = boldStyle
	}

	l := p.Sub(barX - contentX)
	if it.groupCount > 1 {
		if i < 0 || i == 0 {
			branch := "├"
			if it.groupIndex == 0 {
				branch = "┌"
			} else if it.groupLast {
				branch = "└"
			}
			l.Text(branch, dimStyle)
			l.Text(" ", plain)
			l.Text(muteSymbol(n.Mute, n.JACK, focused))
			l.Text(" ", plain)
			if len(n.Channels) > 1 && i >= 0 {
				l.Text("┌", dimStyle)
				l.Text(" ", plain)
			} else {
				l.Text("  ", plain)
			}
		} else {
			if !it.groupLast {
				l.Text("│", dimStyle)
				l.Text(" ", plain)
			} else {
				l.Text("  ", plain)
			}
			l.Text("   ", plain)
			bracket := "├"
			if i == len(n.Channels)-1 {
				bracket = "└"
			}
			l.Text(bracket, dimStyle)
			l.Text(" ", plain)
		}
	} else {
		if i < 0 || len(n.Channels) <= 1 {
			l.Text("       ", plain)
		} else {
			bracket := "├"
			if i == 0 {
				bracket = "┌"
			} else if i == len(n.Channels)-1 {
				bracket = "└"
			}
			l.Text("     ", plain)
			l.Text(bracket, dimStyle)
			l.Text(" ", plain)
		}
	}
	l.Repeat(" ", labelWidth-ansi.StringWidth(name), labelStyle)
	l.Text(name, labelStyle)
	l.Text(" ", plain)
	if n.JACK {
		l.Text("   ", labelStyle)
	} else {
		l.Text(fmt.Sprintf("%3d", percent(c.Volume)), labelStyle)
	}
	l.Pad(plain)
	p.Join(l)

	if m.barWidth() > 0 {
		m.meterRows = append(m.meterRows, meterRow{y: y, it: it, channel: i})
		m.drawChannelBar(p, it, i)
	}
}

// drawChannelBar draws the fader or meter of channel i of it (or its folded
// channels when i is -1) into p, which spans the bar.
func (m *mixer) drawChannelBar(p *tui.Pen, it item, i int) {
	n := it.node
	if n.JACK {
		drawMeterBar(p, m.level(it, i), m.peak(it, i), m.barWidth(), m.opts.limit, m.look())
		return
	}
	c := n.Channels[max(i, 0)]
	drawBar(p, c.Volume, m.level(it, i), m.peak(it, i), m.barWidth(), m.opts.limit, n.Mute, m.look())
}

const (
	faderFull  = "▓"
	faderKnob  = "█"
	faderEmpty = "░"
)

// drawBarRuns calls p.Repeat for each contiguous run in [0, n), splitting
// only at the positions where cell(i) can change.
func drawBarRuns(p *tui.Pen, n, hundred, lit, pk int, look meterLook, cell func(i int) (string, tui.Style)) {
	if n <= 0 {
		return
	}
	var pts [6]int
	k := 0
	add := func(x int) {
		if x > 0 && x < n {
			pts[k] = x
			k++
		}
	}
	add(hundred + 1)
	if lit > 0 {
		add(lit)
		if look == meterDot {
			add(lit - 1)
		}
	}
	if pk > 0 {
		add(pk - 1)
		add(pk)
	}
	pts[k] = n
	k++
	for i := 1; i < k; i++ {
		v := pts[i]
		j := i - 1
		for j >= 0 && pts[j] > v {
			pts[j+1] = pts[j]
			j--
		}
		pts[j+1] = v
	}
	start := 0
	for _, end := range pts[:k] {
		if end <= start {
			continue
		}
		glyph, st := cell(start)
		p.Repeat(glyph, end-start, st)
		start = end
	}
}

// drawMeterBar draws a level meter of width cells without a volume fader.
func drawMeterBar(p *tui.Pen, level, peak float64, width int, limit float64, look meterLook) {
	if width <= 0 {
		return
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
		p.Repeat(faderEmpty, width, dimStyle)
		return
	}
	hundred := knobCell(1, limit, width)
	drawBarRuns(p, width, hundred, lit, pk, look, func(i int) (string, tui.Style) {
		full, bright := barFullStyle, brightFull
		if i > hundred {
			full, bright = barOverStyle, brightOver
		}
		switch {
		case i == pk-1, i == lit-1 && look == meterDot:
			return faderKnob, bright
		case i < lit && (look == meterSolid || look == meterShade):
			return faderKnob, full
		default:
			return faderEmpty, dimStyle
		}
	})
}

var (
	emptyOverStyle = tui.Style{Fg: tui.Red, Attrs: tui.Faint}
	brightFull     = tui.Style{Fg: tui.BrightGreen}
	brightOver     = tui.Style{Fg: tui.BrightRed}
)

// drawBar draws a fader of width cells for volume v, level, and peak on
// [0, limit].
func drawBar(p *tui.Pen, v, level, peak float64, width int, limit float64, muted bool, look meterLook) {
	if width <= 0 {
		return
	}
	knob := knobCell(v, limit, width)
	hundred := knobCell(1, limit, width)
	if muted {
		p.Repeat(faderFull, knob, dimStyle)
		p.Repeat(faderKnob, 1, dimStyle)
		p.Repeat(faderEmpty, width-knob-1, dimStyle)
		return
	}
	knobStyle := barFullStyle
	if knob > hundred {
		knobStyle = barOverStyle
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
	drawBarRuns(p, knob, hundred, lit, pk, look, func(i int) (string, tui.Style) {
		full, bright := barFullStyle, brightFull
		if i > hundred {
			full, bright = barOverStyle, brightOver
		}
		switch {
		case i == pk-1, i == lit-1 && look == meterDot:
			return faderKnob, bright
		case i < lit && (look == meterSolid || look == meterShade):
			return faderKnob, full
		case look == meterShade:
			return "▒", full
		default:
			return faderFull, full
		}
	})
	p.Repeat(faderKnob, 1, knobStyle)
	p.Repeat(faderEmpty, max(knob+1, hundred+1)-(knob+1), dimStyle)
	p.Repeat(faderEmpty, width-max(knob+1, hundred+1), emptyOverStyle)
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

// menuBox returns the width inside the menu's box and the box's position
// and size.
func (m *mixer) menuBox() (inner, x, y, w, h int) {
	mn := m.menu
	inner = ansi.StringWidth(mn.title)
	for _, it := range mn.items {
		inner = max(inner, ansi.StringWidth(it.label)+2)
	}
	inner = max(min(inner, m.width-4), 1)
	first, n := m.menuRows()
	w, h = inner+4, min(first+n, len(mn.items))-first+3
	return inner, max((m.width-w)/2, 0), listTop + max(m.listHeight()-h, 0)/2, w, h
}

// drawMenu draws the menu over the screen.
func (m *mixer) drawMenu(g *tui.Grid) {
	mn := m.menu
	inner, x, y, w, h := m.menuBox()
	edge := func(row int, left, right string) {
		p := g.Pen(x, y+row, x+w)
		p.Text(left, plain)
		p.Repeat("─", w-2, plain)
		p.Text(right, plain)
	}
	row := func(row int, text string, st, pad tui.Style) {
		p := g.Pen(x, y+row, x+w)
		p.Text("│ ", plain)
		p.Fit(text, inner, st, pad)
		p.Text(" │", plain)
	}
	edge(0, "╭", "╮")
	row(1, mn.title, boldStyle, plain)
	first, _ := m.menuRows()
	for r := 2; r < h-1; r++ {
		i := first + r - 2
		it := mn.items[i]
		mark := "  "
		if it.active {
			mark = "● "
		}
		st := plain
		switch {
		case i == mn.cursor:
			st = menuCursorStyle
		case it.dim:
			st = dimStyle
		}
		row(r, mark+it.label, st, st)
	}
	edge(h-1, "╰", "╯")
}
