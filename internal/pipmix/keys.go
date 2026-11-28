package pipmix

import (
	"slices"
)

type binding struct {
	keys  []string
	help  string
	mouse string
	do    func(m *mixer) command
}

type section struct {
	title    string
	bindings []binding
}

func volumeKeys(o options) section {
	return section{"Volume", []binding{
		{[]string{"l", "right", "+", "="}, "Louder by " + percentText(o.step), "click or drag on a bar", func(m *mixer) command { return m.changeVolume(m.opts.step) }},
		{[]string{"h", "left", "-"}, "Quieter by " + percentText(o.step), "", func(m *mixer) command { return m.changeVolume(-m.opts.step) }},
		{[]string{"L", "shift+right"}, "Louder by " + percentText(o.fineStep), "", func(m *mixer) command { return m.changeVolume(m.opts.fineStep) }},
		{[]string{"H", "shift+left"}, "Quieter by " + percentText(o.fineStep), "", func(m *mixer) command { return m.changeVolume(-m.opts.fineStep) }},
		{[]string{"c", "u"}, "Set volume to 100%", "middle click on a bar", (*mixer).setVolume100},
		{[]string{"m"}, "Mute or unmute", "click 🔊, or right click", (*mixer).toggleMute},
		{[]string{"space"}, "Lock or unlock the channels", "", (*mixer).toggleLock},
	}}
}

var portKey = binding{[]string{"p"}, "Choose the port", "click the port", (*mixer).openRouteMenu}

func deviceKeys() section {
	return section{"Devices", []binding{
		portKey,
		{[]string{"D"}, "Make it the default", "", (*mixer).setDefault},
	}}
}

// defaultTabDeviceKeys are deviceKeys for the Default tab, where p also
// plays or pauses a stream.
func defaultTabDeviceKeys() section {
	return section{"Devices", []binding{
		{[]string{"p"}, "Choose the port", "click the port", (*mixer).portOrPlayPause},
		{[]string{"D"}, "Make it the default", "", (*mixer).setDefault},
	}}
}

func playbackKeys() section {
	return section{"Playback", []binding{
		{[]string{"s"}, "Solo stream (mute others)", "", (*mixer).toggleSolo},
		{[]string{"p"}, "Play or pause", "click " + symPlay + " or " + symPause, (*mixer).togglePlayPause},
	}}
}

func cardKeys() section {
	return section{"Cards", []binding{
		{[]string{"P"}, "Choose the profile", "click the profile line", (*mixer).openProfileMenu},
	}}
}

func navigationKeys() section {
	page := func(f func(n int) int) func(m *mixer) command {
		return func(m *mixer) command {
			m.moveItems(f(m.pageItems()))
			return nil
		}
	}
	return section{"Navigation", []binding{
		{[]string{"k", "up", "ctrl+p"}, "Move up", "click", func(m *mixer) command { m.move(-1); return nil }},
		{[]string{"j", "down", "ctrl+n"}, "Move down", "", func(m *mixer) command { m.move(1); return nil }},
		{[]string{"ctrl+b", "pgup", "alt+v"}, "Page up", "", page(func(n int) int { return -n })},
		{[]string{"ctrl+f", "pgdown", "ctrl+v"}, "Page down", "", page(func(n int) int { return n })},
		{[]string{"ctrl+u"}, "Half page up", "", page(func(n int) int { return -max(n/2, 1) })},
		{[]string{"ctrl+d"}, "Half page down", "", page(func(n int) int { return max(n/2, 1) })},
		{[]string{"g", "home"}, "Go to top", "", func(m *mixer) command { m.moveItems(-1 << 30); return nil }},
		{[]string{"G", "end"}, "Go to bottom", "", func(m *mixer) command { m.moveItems(1 << 30); return nil }},
	}}
}

func tabKeys() section {
	set := func(f func(tab) tab) func(m *mixer) command {
		return func(m *mixer) command {
			m.setTab(f(m.tab))
			return nil
		}
	}
	var bindings []binding
	for t := range tabCount {
		key := string(rune('1' + t))
		mouse := ""
		if t == 0 {
			mouse = "click a tab"
		}
		bindings = append(bindings, binding{[]string{key}, tabNames[t], mouse, set(func(tab) tab { return t })})
	}
	bindings = append(bindings,
		binding{[]string{"tab", "t"}, "Next tab", "", set(func(t tab) tab { return (t + 1) % tabCount })},
		binding{[]string{"shift+tab", "T"}, "Previous tab", "", set(func(t tab) tab { return (t + tabCount - 1) % tabCount })},
	)
	return section{"Tabs", bindings}
}

func generalKeys() section {
	return section{"General", []binding{
		{[]string{"?"}, "Help for this tab", "", func(m *mixer) command {
			m.help, m.helpOff = true, 0
			return nil
		}},
		{[]string{"a"}, "Meters on or off", "", (*mixer).toggleMeters},
		{[]string{"A"}, "Meter always or only focused", "", (*mixer).toggleMeterAlways},
	}}
}

func menuKeys() section {
	return section{"Menu", []binding{
		{[]string{"k", "up", "ctrl+p"}, "Move up", "wheel", func(m *mixer) command { m.moveMenu(-1); return nil }},
		{[]string{"j", "down", "ctrl+n"}, "Move down", "", func(m *mixer) command { m.moveMenu(1); return nil }},
		{[]string{"g", "home"}, "Go to top", "", func(m *mixer) command { m.moveMenu(-len(m.menu.items)); return nil }},
		{[]string{"G", "end"}, "Go to bottom", "", func(m *mixer) command { m.moveMenu(len(m.menu.items)); return nil }},
		{[]string{"enter", "space"}, "Choose", "click", func(m *mixer) command { m.choose(); return nil }},
		{[]string{"esc", "q"}, "Close", "click outside", func(m *mixer) command { m.menu = nil; return nil }},
	}}
}

func helpScreenKeys() section {
	return section{"Help", []binding{
		{[]string{"?", "q", "esc"}, "Close help", "", func(m *mixer) command { m.help = false; return nil }},
		{[]string{"k", "up", "ctrl+p"}, "Scroll up", "wheel", func(m *mixer) command { m.scrollHelp(-1); return nil }},
		{[]string{"j", "down", "ctrl+n"}, "Scroll down", "", func(m *mixer) command { m.scrollHelp(1); return nil }},
		{[]string{"ctrl+b", "pgup", "alt+v"}, "Page up", "", func(m *mixer) command { m.scrollHelp(-m.listHeight()); return nil }},
		{[]string{"ctrl+f", "pgdown", "ctrl+v"}, "Page down", "", func(m *mixer) command { m.scrollHelp(m.listHeight()); return nil }},
		{[]string{"g", "home"}, "Go to top", "", func(m *mixer) command { m.scrollHelp(-1 << 30); return nil }},
		{[]string{"G", "end"}, "Go to bottom", "", func(m *mixer) command { m.scrollHelp(1 << 30); return nil }},
	}}
}

// tabSections returns the keybinding sections for the current tab.
func (m *mixer) tabSections() []section {
	var own []section
	switch m.tab {
	case tabDefault:
		own = []section{volumeKeys(m.opts), defaultTabDeviceKeys(), playbackKeys()}
	case tabPlayback:
		own = []section{volumeKeys(m.opts), playbackKeys()}
	case tabOutputs, tabInputs:
		own = []section{volumeKeys(m.opts), deviceKeys()}
	case tabCards:
		own = []section{cardKeys()}
	default:
		own = []section{volumeKeys(m.opts)}
	}
	general := generalKeys()
	general.bindings = append(general.bindings, m.quitKeys.bindings...)
	sections := append(own, navigationKeys(), tabKeys(), general)
	if !m.opts.mouse {
		for _, s := range sections {
			for i := range s.bindings {
				s.bindings[i].mouse = ""
			}
		}
	}
	return sections
}

// sections returns the active keybinding sections.
func (m *mixer) sections() []section {
	switch {
	case m.menu != nil:
		return []section{menuKeys(), m.quitKeys}
	case m.help:
		return []section{helpScreenKeys(), tabKeys(), m.quitKeys}
	}
	return m.tabSections()
}

func (m *mixer) handleKey(key string) command {
	for _, s := range m.sections() {
		for _, b := range s.bindings {
			if slices.Contains(b.keys, key) {
				return b.do(m)
			}
		}
	}
	return nil
}

func (m *mixer) moveMenu(delta int) {
	m.menu.cursor = max(0, min(m.menu.cursor+delta, len(m.menu.items)-1))
}
