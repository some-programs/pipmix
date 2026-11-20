package pipmix

import (
	"math"
	"time"

	"github.com/some-programs/pipmix/internal/tui"
)

const wheelRows = 3

func (m *mixer) handleMouse(ev tui.MouseEvent) command {
	if m.menu != nil {
		return m.menuMouse(ev)
	}
	switch ev.Kind {
	case tui.MouseClick:
		return m.click(ev)
	case tui.MouseMotion:
		m.dragTo(ev)
	case tui.MouseRelease:
		m.flushDrag(true)
		m.drag = nil
	case tui.MouseWheel:
		delta := wheelRows
		if ev.Button == tui.MouseWheelUp {
			delta = -delta
		}
		if m.help {
			m.scrollHelp(delta)
		} else {
			m.scrollBy(delta)
		}
	}
	return nil
}

func (m *mixer) click(mouse tui.MouseEvent) command {
	if mouse.Y == 0 && mouse.Button == tui.MouseLeft {
		for _, s := range tabSpans() {
			if mouse.X >= s.start && mouse.X < s.end {
				m.setTab(s.tab)
			}
		}
		return nil
	}
	if m.help {
		return nil
	}
	items, i, line, ok := m.hit(mouse.Y)
	if !ok {
		return nil
	}
	m.setFocus(items, i, line.channel)
	it := items[i]
	switch mouse.Button {
	case tui.MouseMiddle:
		switch line.kind {
		case lineChannel:
			if mouse.X < barX-2 || mouse.X >= barX+m.barWidth() {
				return nil
			}
			if it.node.JACK {
				return m.setVolume100()
			}
			channel := -1
			if m.isUnlocked(it) {
				channel = line.channel
			}
			m.client.SetVolume(it.id, channel, 1.0, m.opts.limit)
		}
	case tui.MouseRight:
		return m.toggleMute()
	case tui.MouseLeft:
		if onStreamMuteButton(it, line, mouse.X) {
			m.client.ToggleMute(it.id)
			return nil
		}
		if onMuteButton(it, line, mouse.X) {
			if it.groupCount > 1 {
				m.toggleGroupMute(it.groupKey)
				return nil
			}
			return m.toggleMute()
		}
		if onPlayButton(it, line, mouse.X) {
			return m.togglePlayPause()
		}
		if start, end, ok := m.portSpan(it); ok && line.kind == lineTitle && mouse.X >= start && mouse.X < end {
			return m.openRouteMenu()
		}
		switch line.kind {
		case lineChannel:
			if mouse.X < barX-2 || mouse.X >= barX+m.barWidth() {
				return nil
			}
			if it.node.JACK {
				return m.setVolume100()
			}
			channel := -1
			if m.isUnlocked(it) {
				channel = line.channel
			}
			v := m.volumeAt(mouse.X)
			m.drag = &drag{node: it.id, channel: channel, last: v, sent: v, sentAt: time.Now()}
			m.client.SetVolume(it.id, channel, v, m.opts.limit)
		case lineProfile:
			return m.openProfileMenu()
		}
	}
	return nil
}

func (m *mixer) dragInterval() time.Duration {
	return time.Second / time.Duration(max(m.opts.fps, 1))
}

func (m *mixer) dragTo(mouse tui.MouseEvent) {
	d := m.drag
	if d == nil || mouse.Button != tui.MouseLeft {
		return
	}
	v := m.volumeAt(mouse.X)
	if v == d.last {
		return
	}
	d.last = v
	if now := time.Now(); now.Sub(d.sentAt) >= m.dragInterval() {
		d.sent, d.sentAt = v, now
		m.client.SetVolume(d.node, d.channel, v, m.opts.limit)
	}
}

// flushDrag sends a throttled drag volume once the frame interval has
// elapsed, or immediately when force is true.
func (m *mixer) flushDrag(force bool) {
	d := m.drag
	if d == nil || d.last == d.sent {
		return
	}
	now := time.Now()
	if !force && now.Sub(d.sentAt) < m.dragInterval() {
		return
	}
	d.sent, d.sentAt = d.last, now
	m.client.SetVolume(d.node, d.channel, d.last, m.opts.limit)
}

func (m *mixer) volumeAt(x int) float64 {
	return cellVolume(x-barX, m.barWidth(), m.opts.limit)
}

// cellVolume returns the volume corresponding to cell c of a fader of width
// cells, snapping to 1.0 when within half a cell of 100%.
func cellVolume(c, width int, limit float64) float64 {
	if width <= 1 || c < 0 {
		return 0
	}
	c = min(c, width-1)
	perCell := limit / float64(width-1)
	v := float64(c) * perCell
	if math.Abs(v-1) <= perCell/2 {
		return 1
	}
	return math.Round(v*100) / 100
}

// hit returns the item and line at screen row y.
func (m *mixer) hit(y int) (items []item, i int, line itemLine, ok bool) {
	row := y - listTop
	if row < 0 || row >= m.listHeight() {
		return nil, 0, itemLine{}, false
	}
	row += m.offset[m.tab]
	items = m.items(m.tab)
	for i, it := range items {
		lines := m.itemLines(it)
		if row < len(lines) {
			return items, i, lines[row], true
		}
		row -= len(lines)
	}
	return nil, 0, itemLine{}, false
}

// scrollBy scrolls the list by delta lines and keeps focus on a visible item.
func (m *mixer) scrollBy(delta int) {
	items := m.items(m.tab)
	if len(items) == 0 {
		return
	}
	tops := make([]int, len(items)+1)
	for i, it := range items {
		tops[i+1] = tops[i] + m.itemHeight(it)
	}
	h := m.listHeight()
	off := max(0, min(m.offset[m.tab]+delta, tops[len(items)]-h))
	m.offset[m.tab] = off
	visible := func(i int) bool { return tops[i] >= off && tops[i+1] <= off+h }
	i := m.focusIndex(m.tab, items)
	if visible(i) {
		return
	}
	best := -1
	for j := range items {
		if visible(j) && (best < 0 || abs(j-i) < abs(best-i)) {
			best = j
		}
	}
	if best < 0 {
		for j := range items {
			if tops[j+1] > off {
				best = j
				break
			}
		}
	}
	m.setFocus(items, best, 0)
}

func abs(n int) int { return max(n, -n) }

func (m *mixer) menuMouse(mouse tui.MouseEvent) command {
	switch mouse.Kind {
	case tui.MouseClick:
		_, x, y, w, h := m.menuBox()
		if mouse.X < x || mouse.X >= x+w || mouse.Y < y || mouse.Y >= y+h {
			m.menu = nil
			return nil
		}
		// Rows: the border, the title, then the items.
		first, n := m.menuRows()
		if k := mouse.Y - y - 2; mouse.Button == tui.MouseLeft && k >= 0 && k < n && first+k < len(m.menu.items) {
			m.menu.cursor = first + k
			m.choose()
		}
	case tui.MouseWheel:
		if mouse.Button == tui.MouseWheelUp {
			m.moveMenu(-1)
		} else {
			m.moveMenu(1)
		}
	}
	return nil
}
