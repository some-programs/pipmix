package pipmix

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/some-programs/pipmix/internal/mpris"
	"github.com/some-programs/pipmix/internal/pw"
)

func stereo(v float64) []pw.Channel {
	return []pw.Channel{{Name: "FL", Volume: v}, {Name: "FR", Volume: v}}
}

func testSnapshot() *pw.Snapshot {
	return &pw.Snapshot{
		Connected: true,
		Nodes: []pw.Node{
			{ID: 10, Class: pw.Playback, Name: "a", Title: "Player A", Channels: stereo(0.5)},
			{ID: 11, Class: pw.Playback, Name: "b", Title: "Player B", Channels: stereo(1)},
			{ID: 12, Class: pw.Playback, Name: "c", Title: "Player C", Channels: stereo(1.2), Mute: true},
			{
				ID: 20, Class: pw.Sink, Name: "sink", Title: "Speakers", Channels: stereo(1), Default: true,
				Routes: []pw.Route{{Index: 0, Description: "Speakers"}, {Index: 1, Description: "Headphones"}}, Route: 1,
			},
		},
		Cards: []pw.Card{{ID: 30, Title: "Card", Profile: 0, Profiles: []pw.Profile{
			{Index: 0, Description: "Duplex"}, {Index: 1, Description: "Off"},
		}}},
	}
}

func testMixer() mixer {
	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	m.snap = testSnapshot()
	m.setSize(60, 20)
	m.refocus()
	m.setTab(tabPlayback)
	return m
}

func focusedID(m *mixer) uint32 {
	it, _ := m.focused()
	return it.id
}

func TestMoveLockedAndUnlocked(t *testing.T) {
	m := testMixer()
	m.handleKey("j")
	if id := focusedID(&m); id != 11 {
		t.Fatalf("after j: focus %d, want 11", id)
	}
	// Unlocked, j and k go through the channels before leaving the item.
	m.handleKey("space")
	m.handleKey("j")
	if id, ch := focusedID(&m), m.focus[m.tab].channel; id != 11 || ch != 1 {
		t.Fatalf("unlocked j: focus %d channel %d, want 11 channel 1", id, ch)
	}
	m.handleKey("j")
	if id := focusedID(&m); id != 12 {
		t.Fatalf("j past the last channel: focus %d, want 12", id)
	}
	// Coming back up enters the unlocked item at its last channel.
	m.handleKey("k")
	if id, ch := focusedID(&m), m.focus[m.tab].channel; id != 11 || ch != 1 {
		t.Fatalf("k into unlocked item: focus %d channel %d, want 11 channel 1", id, ch)
	}
	m.handleKey("G")
	if id := focusedID(&m); id != 12 {
		t.Errorf("G: focus %d, want 12", id)
	}
	m.handleKey("g")
	if id := focusedID(&m); id != 10 {
		t.Errorf("g: focus %d, want 10", id)
	}
}

func TestFocusSurvivesUpdates(t *testing.T) {
	m := testMixer()
	m.handleKey("j") // Player B

	// A new item before the focused one doesn't move the focus off it.
	s := testSnapshot()
	s.Nodes = append([]pw.Node{{ID: 5, Class: pw.Playback, Title: "New"}}, s.Nodes...)
	m.snap = s
	m.refocus()
	if id := focusedID(&m); id != 11 {
		t.Fatalf("after insert: focus %d, want 11", id)
	}

	// When the focused item goes, the one now in its place takes over.
	s = testSnapshot()
	s.Nodes = append([]pw.Node{{ID: 5, Class: pw.Playback, Title: "New"}}, s.Nodes...)
	s.Nodes = append(s.Nodes[:2], s.Nodes[3:]...) // drop 11
	m.snap = s
	m.refocus()
	if id := focusedID(&m); id != 12 {
		t.Fatalf("after removal: focus %d, want 12", id)
	}

	// An empty tab, as while disconnected, then items again.
	m.snap = &pw.Snapshot{Connected: true}
	m.refocus()
	if _, i := m.focused(); i != -1 {
		t.Fatalf("empty tab: focus index %d", i)
	}
	// When it comes back, as after a reconnect, so does the focus.
	m.snap = testSnapshot()
	m.refocus()
	if id := focusedID(&m); id != 12 {
		t.Fatalf("refilled: focus %d, want 12", id)
	}
}

func TestMenus(t *testing.T) {
	m := testMixer()
	m.handleKey("p")
	if m.menu != nil {
		t.Fatal("route menu opened on a stream")
	}
	m.handleKey("4")
	m.handleKey("p")
	if m.menu == nil || len(m.menu.items) != 2 || m.menu.cursor != 1 {
		t.Fatalf("route menu = %+v", m.menu)
	}
	if !strings.Contains(m.view(), "Headphones") {
		t.Error("menu not drawn")
	}
	m.handleKey("k")
	m.handleKey("esc")
	if m.menu != nil {
		t.Fatal("esc didn't close the menu")
	}

	// The menu goes when its target does.
	m.handleKey("p")
	s := testSnapshot()
	s.Nodes = s.Nodes[:3]
	m.snap = s
	m.refocus()
	if m.menu != nil {
		t.Error("menu stayed open for a removed node")
	}

	m.handleKey("6")
	m.handleKey("P")
	if m.menu == nil || m.menu.kind != menuProfile || m.menu.cursor != 0 {
		t.Fatalf("profile menu = %+v", m.menu)
	}
}

func TestViewWidths(t *testing.T) {
	m := testMixer()
	for _, w := range []int{60, 30, 8} {
		m.setSize(w, 20)
		for _, key := range []string{"1", "2", "4", "6"} {
			m.handleKey(key)
			lines := strings.Split(m.view(), "\n")
			if len(lines) != 20 {
				t.Errorf("width %d tab %s: %d lines, want 20", w, key, len(lines))
			}
			for i, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("width %d tab %s line %d is %d wide: %q", w, key, i, lw, ansi.Strip(l))
				}
			}
		}
	}
}

func TestScrollKeepsFocusVisible(t *testing.T) {
	m := testMixer()
	m.setSize(60, 4) // two rows: room for one item of two lines
	m.handleKey("G")
	view := ansi.Strip(m.view())
	if !strings.Contains(view, "Player C") || strings.Contains(view, "Player A") {
		t.Errorf("view after G:\n%s", view)
	}
	m.handleKey("g")
	if view := ansi.Strip(m.view()); !strings.Contains(view, "Player A") {
		t.Errorf("view after g:\n%s", view)
	}
}

func TestDefaultFirst(t *testing.T) {
	m := testMixer()
	s := testSnapshot()
	s.Nodes = append(s.Nodes, pw.Node{ID: 21, Class: pw.Sink, Name: "hdmi", Title: "HDMI", Channels: stereo(1)})
	s.Nodes[3].Default = false
	m.snap = s
	m.handleKey("4")
	m.refocus()
	if id := focusedID(&m); id != 20 {
		t.Fatalf("before default: focus %d, want 20", id)
	}
	s.Nodes[4].Default = true
	m.refocus()
	items := m.items(tabOutputs)
	if len(items) != 2 || items[0].id != 21 || items[1].id != 20 {
		t.Fatalf("items = %v %v, want 21 first", items[0].id, items[1].id)
	}
	if id := focusedID(&m); id != 21 {
		t.Errorf("unmoved focus = %d, want new default 21", id)
	}
	m.handleKey("j")
	s.Nodes[3].Default, s.Nodes[4].Default = true, false
	m.refocus()
	if it, i := m.focused(); it.id != 20 || i != 0 {
		t.Errorf("chosen focus = %d at %d, want 20 at 0", it.id, i)
	}
}

func TestQuitKeysAndLayers(t *testing.T) {
	m := testMixer()
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	m.handleKey("?")
	if !m.help {
		t.Fatal("? didn't open the help")
	}
	if isQuit(m.handleKey("q")) || m.help {
		t.Fatal("q on the help should close it, not quit")
	}
	m.handleKey("4")
	m.handleKey("p")
	if isQuit(m.handleKey("q")) || m.menu != nil {
		t.Fatal("q on a menu should close it, not quit")
	}
	if isQuit(m.handleKey("esc")) {
		t.Fatal("esc quit")
	}
	if !isQuit(m.handleKey("q")) {
		t.Fatal("q didn't quit")
	}
}

func TestHelp(t *testing.T) {
	m := testMixer()
	m.setSize(120, 40)
	m.handleKey("4")
	m.handleKey("?")
	view := ansi.Strip(m.view())
	for _, want := range []string{"Volume", "Louder by 5%", "click or drag on a bar", "Devices", "Choose the port", "Tabs", "Quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("help on Output Devices lacks %q:\n%s", want, view)
		}
	}
	m.handleKey("6") // the help follows the tab
	if view := ansi.Strip(m.view()); !strings.Contains(view, "Choose the profile") || strings.Contains(view, "Louder") {
		t.Errorf("help on Cards:\n%s", view)
	}
	// It scrolls when it doesn't fit.
	m.setSize(30, 10)
	m.handleKey("G")
	if m.helpOff == 0 {
		t.Error("G didn't scroll the help")
	}
}

func TestMouse(t *testing.T) {
	m := testMixer()
	m.setSize(64, 20) // bars from column 20, 42 wide
	if w := m.barWidth(); w != 42 {
		t.Fatalf("bar width %d", w)
	}
	// 42 cells: the knob moves 1.5/41 a cell, and 100% is cell 27.
	for _, tc := range []struct {
		x    int
		want float64
	}{
		{barX - 1, 0}, // dragged off the start
		{barX, 0},
		{barX + 14, 0.51},
		{barX + 26, 0.95},
		{barX + 27, 1}, // 98.8%, within half a cell of 100%
		{barX + 41, 1.5},
		{barX + 100, 1.5}, // dragged off the end
	} {
		if got := m.volumeAt(tc.x); got != tc.want {
			t.Errorf("volumeAt(%d) = %v, want %v", tc.x, got, tc.want)
		}
	}

	// Rows: 0 tabs, then Player A at 1-2 (title, FL+FR folded), Player B
	// at 3-4.
	click := func(x, y int, b tea.MouseButton) {
		m.handleMouse(tea.MouseClickMsg{X: x, Y: y, Button: b})
	}
	click(4, 3, tea.MouseLeft)
	if id := focusedID(&m); id != 11 {
		t.Fatalf("click on Player B's title: focus %d", id)
	}
	click(barX+5, 2, tea.MouseLeft)
	if id := focusedID(&m); id != 10 || m.drag == nil || m.drag.node != 10 || m.drag.channel != -1 {
		t.Fatalf("click on Player A's bar: focus %d, drag %+v", id, m.drag)
	}
	m.handleMouse(tea.MouseMotionMsg{X: barX + 20, Y: 2, Button: tea.MouseLeft})
	if want := m.volumeAt(barX + 20); m.drag.last != want {
		t.Errorf("drag: last %v, want %v", m.drag.last, want)
	}
	m.handleMouse(tea.MouseReleaseMsg{X: barX + 20, Y: 2, Button: tea.MouseLeft})
	if m.drag != nil {
		t.Error("release didn't end the drag")
	}

	// The mute button: the first two cells of the title line.
	items, i, line, _ := m.hit(1)
	if !onMuteButton(items[i], line, muteX) || !onMuteButton(items[i], line, muteX+1) || onMuteButton(items[i], line, muteX+2) {
		t.Error("the mute button isn't at columns 2-3 of the title line")
	}
	if view := ansi.Strip(m.view()); !strings.Contains(view, symSpeaker) || !strings.Contains(view, "Player A") || !strings.Contains(view, symMuted) || !strings.Contains(view, "Player C") {
		t.Errorf("mute buttons not drawn:\n%s", view)
	}

	// A tab.
	spans := tabSpans()
	click(spans[tabCards].start, 0, tea.MouseLeft)
	if m.tab != tabCards {
		t.Fatalf("click on the Cards tab: tab %d", m.tab)
	}
	// The profile line opens the menu; a click outside closes it.
	click(5, 2, tea.MouseLeft)
	if m.menu == nil || m.menu.kind != menuProfile {
		t.Fatalf("click on the profile line: menu %+v", m.menu)
	}
	click(0, m.height-1, tea.MouseLeft)
	if m.menu != nil {
		t.Error("click outside didn't close the menu")
	}
}

func TestWheelPullsFocus(t *testing.T) {
	m := testMixer()
	m.setSize(60, 4) // three rows: room for one item of two lines
	m.handleMouse(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if id := focusedID(&m); id != 12 {
		t.Errorf("after scrolling 3 lines: focus %d, want 12, the only item fully on screen", id)
	}
	if !strings.Contains(ansi.Strip(m.view()), "Player C") {
		t.Errorf("view:\n%s", ansi.Strip(m.view()))
	}
}

func TestFold(t *testing.T) {
	m := testMixer()
	lines := func() []string {
		var out []string
		for l := range strings.SplitSeq(ansi.Strip(m.view()), "\n") {
			if strings.Contains(l, "FL") || strings.Contains(l, "FR") || strings.Contains(l, "ch ") {
				out = append(out, strings.TrimSpace(l))
			}
		}
		return out
	}
	raw := m.view()
	if !strings.Contains(raw, boldStyle.Render("FL+FR")) || !strings.Contains(raw, boldStyle.Render(" 50")) {
		t.Errorf("focused folded channel name or volume not bold:\n%s", raw)
	}
	if got := lines(); len(got) != 3 || !strings.HasPrefix(got[0], "█        FL+FR  50") {
		t.Fatalf("folded lines = %q", got)
	}
	m.handleKey("space")
	if got := lines(); len(got) != 4 || !strings.Contains(got[0], "┌    FL  50") || !strings.Contains(got[1], "└    FR  50") {
		t.Fatalf("unlocked lines = %q", got)
	}
	m.handleKey("space")
	s := testSnapshot()
	s.Nodes[0].Channels[1].Volume = 0.4
	m.snap = s
	m.refocus()
	if got := lines(); len(got) != 4 || !strings.Contains(got[0], "┌    FL  50") || !strings.Contains(got[1], "└    FR  40") {
		t.Fatalf("unequal lines = %q", got)
	}
	six := make([]pw.Channel, 6)
	for i, n := range []string{"FL", "FR", "FC", "LFE", "RL", "RR"} {
		six[i] = pw.Channel{Name: n, Volume: 1}
	}
	if got := foldedName(six); got != "6ch" {
		t.Errorf("foldedName(5.1) = %q", got)
	}
	s = testSnapshot()
	m.snap = s
	m.refocus()
	m.handleMouse(tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft})
	if id := focusedID(&m); id != 11 {
		t.Errorf("click on Player B's folded line: focus %d", id)
	}
	if _, ch, _ := m.volumeTarget(); ch != -1 {
		t.Errorf("volume target channel %d, want all", ch)
	}
}

func TestCardsSpaced(t *testing.T) {
	m := testMixer()
	s := testSnapshot()
	s.Cards = append(s.Cards, pw.Card{ID: 31, Title: "Second Card", Profile: -1})
	m.snap = s
	m.handleKey("6")
	lines := strings.Split(ansi.Strip(m.view()), "\n")
	if !strings.Contains(lines[1], "Card") || !strings.Contains(lines[2], "Profile") ||
		strings.TrimSpace(lines[3]) != "" || !strings.Contains(lines[4], "Second Card") {
		t.Errorf("cards:\n%s", strings.Join(lines, "\n"))
	}
	if _, _, line, ok := m.hit(3); ok && line.kind != lineBlank {
		t.Errorf("row 3 is %v", line.kind)
	}
}

func TestTitleColumns(t *testing.T) {
	m := testMixer()
	s := testSnapshot()
	s.Nodes = append(s.Nodes, pw.Node{
		ID: 21, Class: pw.Sink, Name: "hdmi", Title: "HDMI Output With A Long Name", Channels: stereo(1),
		Routes: []pw.Route{{Index: 0, Description: "HDMI"}}, Route: 0, Mute: true,
	})
	m.snap = s
	m.handleKey("4")
	lines := strings.Split(ansi.Strip(m.view()), "\n")
	speakers, hdmi := lines[1], lines[3]
	col := func(line, sub string) int { return ansi.StringWidth(line[:strings.LastIndex(line, sub)]) }
	if a, b := col(speakers, "Headphones ▾"), col(hdmi, "HDMI "); a != b {
		t.Errorf("ports not aligned:\n%s\n%s", speakers, hdmi)
	}
	if !strings.HasSuffix(strings.TrimRight(speakers, " "), "◉") || strings.Contains(hdmi, "muted") {
		t.Errorf("tags:\n%q\n%q", speakers, hdmi)
	}
	if a, b := col(speakers, "◉"), ansi.StringWidth(strings.TrimRight(hdmi, " "))+1; a < b {
		t.Errorf("default slot at %d, but HDMI's line runs to %d", a, b)
	}
	start, end, ok := m.portSpan(m.items(tabOutputs)[0])
	if !ok || end-start != ansi.StringWidth("Headphones ▾") || col(speakers, "Headphones") != start {
		t.Fatalf("port span %d-%d %v", start, end, ok)
	}
	m.handleMouse(tea.MouseClickMsg{X: 8, Y: 1, Button: tea.MouseLeft})
	if m.menu != nil {
		t.Fatal("click on the title opened a menu")
	}
	m.handleMouse(tea.MouseClickMsg{X: start, Y: 1, Button: tea.MouseLeft})
	if m.menu == nil || m.menu.kind != menuRoute {
		t.Errorf("click on the port: menu %+v", m.menu)
	}
	m.menu = nil
	m.setSize(45, 10)
	line := strings.Split(ansi.Strip(m.view()), "\n")[1]
	if !strings.Contains(line, "Speakers") || !strings.Contains(line, "Headphone…") || !strings.HasSuffix(strings.TrimRight(line, " "), "◉") {
		t.Errorf("narrow title line %q", line)
	}
}

func TestDefaultTab(t *testing.T) {
	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	s := testSnapshot()
	s.Nodes = append(s.Nodes,
		pw.Node{ID: 21, Class: pw.Sink, Name: "hdmi", Title: "HDMI", Channels: stereo(1)},
		pw.Node{ID: 40, Class: pw.Recording, Name: "rec", Title: "Recorder", Channels: stereo(1)})
	m.snap = s
	m.setSize(60, 20)
	m.refocus()
	if m.tab != tabDefault {
		t.Fatalf("starts on tab %d, want the Default tab", m.tab)
	}
	var ids []uint32
	for _, it := range m.items(tabDefault) {
		ids = append(ids, it.id)
	}
	if want := []uint32{20, 10, 11, 12}; !slices.Equal(ids, want) {
		t.Fatalf("Default tab items = %v, want %v", ids, want)
	}
	if id := focusedID(&m); id != 20 {
		t.Errorf("focus starts on %d, want the default output", id)
	}
	view := ansi.Strip(m.view())
	if !strings.Contains(view, "Headphones ▾ ◉") {
		t.Errorf("default output shows no port and tag:\n%s", view)
	}
	s.Nodes[0].Title = "Long Stream Title 12345678901234567890"
	view = ansi.Strip(m.view())
	if !strings.Contains(view, "Long Stream Title 12345678901234567890") {
		t.Errorf("stream title truncated on Default tab:\n%s", view)
	}
	s.Nodes[0].Title = "Player A"
	m.handleKey("p")
	if m.menu == nil || m.menu.kind != menuRoute {
		t.Errorf("p on the default output: menu = %+v", m.menu)
	}
	s.Nodes[3].Default = false
	m.refocus()
	if items := m.items(tabDefault); len(items) != 3 || items[0].id != 10 {
		t.Errorf("without a default output: %d items, first %d", len(items), items[0].id)
	}
}

func TestTypeStrip(t *testing.T) {
	m := testMixer()
	s := testSnapshot()
	s.Nodes = append(s.Nodes,
		pw.Node{ID: 22, Class: pw.Source, Name: "mic", Title: "Mic", Channels: stereo(1)},
		pw.Node{ID: 40, Class: pw.Recording, Name: "rec", Title: "Recorder", Channels: stereo(1)})
	m.snap = s
	m.refocus()
	strip := func(c pw.Class) string { return classStripStyles[c].Render(typeStripBar) }
	card := cardStripStyle.Render(typeStripBar)
	for _, tc := range []struct {
		tab   tab
		id    uint32
		strip string
	}{
		{tabDefault, 20, strip(pw.Sink)},
		{tabDefault, 10, strip(pw.Playback)},
		{tabPlayback, 10, strip(pw.Playback)},
		{tabRecording, 40, strip(pw.Recording)},
		{tabOutputs, 20, strip(pw.Sink)},
		{tabInputs, 22, strip(pw.Source)},
		{tabCards, 30, card},
	} {
		m.setTab(tc.tab)
		found := false
		for _, it := range m.items(tc.tab) {
			if it.id != tc.id {
				continue
			}
			found = true
			for _, focused := range []bool{false, true} {
				lines := m.renderItem(it, focused)
				if strings.Contains(lines[0], focusTitleStyle.Render(it.title())) != focused {
					t.Errorf("tab %s, item %d, focused %v: title line %q, want the name bold and underlined only when focused", tabNames[tc.tab], tc.id, focused, lines[0])
				}
				for i, l := range lines {
					if l == "" {
						continue
					}
					if !strings.HasPrefix(l, " "+tc.strip) {
						t.Errorf("tab %s, item %d, focused %v, line %d: %q, want it to start %q", tabNames[tc.tab], tc.id, focused, i, l, " "+tc.strip)
					}
					if w := ansi.StringWidth(l); w != m.width {
						t.Errorf("tab %s, item %d, line %d is %d wide, want %d", tabNames[tc.tab], tc.id, i, w, m.width)
					}
				}
			}
		}
		if !found {
			t.Errorf("tab %s has no item %d", tabNames[tc.tab], tc.id)
		}
	}
}

func TestJACKClient(t *testing.T) {
	m := testMixer()
	jack := pw.Node{
		ID: 5, Class: pw.Playback, Name: "dtime", Title: "dtime", JACK: true,
		Channels: []pw.Channel{{Name: "out_1", Volume: 1}, {Name: "out_2", Volume: 1}},
	}
	m.snap.Nodes = append([]pw.Node{jack}, m.snap.Nodes...)
	m.refocus()
	for _, tb := range []tab{tabDefault, tabPlayback} {
		items := m.items(tb)
		if last := items[len(items)-1]; last.id != 5 {
			t.Errorf("tab %d: last item %d, want the JACK client", tb, last.id)
		}
	}
	m.handleKey("G")
	if id := focusedID(&m); id != 5 {
		t.Fatalf("G: focus %d, want 5", id)
	}
	view := ansi.Strip(m.view())
	if !strings.Contains(view, strings.Repeat(faderEmpty, m.barWidth())) || !strings.Contains(view, symSpeaker) || !strings.Contains(view, "JACK: dtime") || !strings.Contains(view, "2ch") {
		t.Errorf("view:\n%s", view)
	}
	// Space unfolds individual JACK output ports to inspect their levels.
	m.handleKey("space")
	view = ansi.Strip(m.view())
	if !strings.Contains(view, "┌ out_1") || !strings.Contains(view, "└ out_2") {
		t.Errorf("unfolded JACK view:\n%s", view)
	}
	m.handleKey("space")
	for _, key := range []string{"l", "m"} {
		cmd := m.handleKey(key)
		if cmd == nil {
			t.Fatalf("%s: no notice", key)
		}
		if msg, ok := cmd().(noticeMsg); !ok || !strings.Contains(msg.text, "JACK client") {
			t.Errorf("%s: %+v, want a JACK notice", key, msg)
		}
	}
	for _, pt := range []struct{ x, y int }{{muteX, 7}, {barX + 2, 8}} {
		cmd := m.click(tea.Mouse{X: pt.x, Y: pt.y, Button: tea.MouseLeft})
		if cmd == nil {
			t.Fatalf("click at (%d,%d): no notice", pt.x, pt.y)
		}
		if msg, ok := cmd().(noticeMsg); !ok || !strings.Contains(msg.text, "JACK client") {
			t.Errorf("click at (%d,%d): %+v, want a JACK notice", pt.x, pt.y, msg)
		}
	}
}

func TestMediaTitlesAndControls(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	players := &mpris.Snapshot{Players: []mpris.Player{
		{Name: "org.mpris.MediaPlayer2.playing", PID: os.Getppid(), Title: "Track 1", Playing: true},
	}}
	nodes := []pw.Node{
		{ID: 10, Class: pw.Playback, Client: "Player", Title: "Player: Playback", Props: map[string]string{"application.process.id": pid}, Channels: stereo(0.5)},
		{ID: 11, Class: pw.Playback, Client: "Other", Title: "Other: Sound", Props: map[string]string{"application.process.id": "1"}, Channels: stereo(0.5)},
		{ID: 12, Class: pw.Recording, Client: "Player", Title: "Player: Capture", Props: map[string]string{"application.process.id": pid}, Channels: stereo(0.5)},
	}
	titles, matched := streamMedia(nodes, players)
	if len(titles) != 1 || titles[10] != "Track 1" || len(matched) != 1 {
		t.Fatalf("streamMedia = %v, %v", titles, matched)
	}

	m := testMixer()
	m.snap = &pw.Snapshot{Connected: true, Nodes: nodes}
	m.mediaTitles = titles
	m.mediaPlayers = matched
	m.refocus()

	if it := m.items(tabRecording)[0]; it.title() != "Player: Capture" {
		t.Errorf("recording title %q", it.title())
	}

	raw := m.view()
	if !strings.Contains(raw, focusStyle.Bold(true).Render(symPause)) {
		t.Errorf("focused toolbar button should be cyan bold:\n%s", raw)
	}
	view := ansi.Strip(raw)
	if !strings.Contains(view, symSpeaker+" "+symPause) || !strings.Contains(view, "Player: Track 1") {
		t.Errorf("view with playing player: %q", view)
	}

	m.mediaPlayers[10] = mpris.Player{Name: "org.mpris.MediaPlayer2.paused", PID: os.Getppid(), Title: "Track 1"}
	view = ansi.Strip(m.view())
	if !strings.Contains(view, symSpeaker+" "+symPlay) {
		t.Errorf("view with paused player: %q", view)
	}

	m.media = mpris.NewWatcher(nil)
	if cmd := m.click(tea.Mouse{X: playX, Y: 1, Button: tea.MouseLeft}); cmd == nil {
		t.Fatal("click on play/pause button returned nil cmd")
	}

	m.move(1)
	cmd := m.handleKey("p")
	if cmd == nil {
		t.Fatal("key p returned nil cmd")
	}
	if msg, ok := cmd().(noticeMsg); !ok || !strings.Contains(msg.text, "no media player") {
		t.Errorf("key p on stream without player: %+v", msg)
	}
}

func TestDedupPlayback(t *testing.T) {
	snap := &pw.Snapshot{
		Connected: true,
		Nodes: []pw.Node{
			{
				ID: 10, Class: pw.Playback, Name: "chrome", Client: "Google Chrome", Title: "Google Chrome: Playback",
				Props: map[string]string{"application.process.id": "1000"}, Channels: stereo(1),
			},
			{
				ID: 11, Class: pw.Playback, Name: "chrome", Client: "Google Chrome", Title: "Google Chrome: Playback",
				Props: map[string]string{"application.process.id": "1000"}, Channels: []pw.Channel{{Name: "MONO", Volume: 1}},
			},
			{
				ID: 12, Class: pw.Playback, Name: "other", Client: "Other", Title: "Other App",
				Props: map[string]string{"application.process.id": "2000"}, Channels: []pw.Channel{{Name: "MONO", Volume: 1}},
			},
		},
	}

	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	m.snap = snap
	items := m.items(tabPlayback)
	if len(items) != 2 || items[0].id != 10 || items[1].id != 12 {
		t.Errorf("dedup enabled: got items %v, want [10, 12]", items)
	}

	opts := defaultOptions()
	opts.dedup = false
	m = newMixer(pw.NewClient(nil), nil, nil, quitKeys(), opts)
	m.snap = snap
	if items := m.items(tabPlayback); len(items) != 3 {
		t.Errorf("dedup disabled: got %d items, want 3", len(items))
	}

	m = newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	m.snap = snap
	m.mediaTitles = map[uint32]string{10: "Video A", 11: "Video B"}
	if items := m.items(tabPlayback); len(items) != 3 {
		t.Errorf("different media titles: got %d items, want 3", len(items))
	}
}

func TestPlaybackGrouping(t *testing.T) {
	snap := &pw.Snapshot{
		Connected: true,
		Nodes: []pw.Node{
			{
				ID: 1, Class: pw.Playback, Name: "mpd", Client: "mpd", Title: "mpd: Basic Channel",
				Props: map[string]string{"application.process.id": "500"}, Channels: stereo(0.8),
			},
			{
				ID: 10, Class: pw.Playback, Name: "chrome1", Client: "Google Chrome", Title: "Google Chrome: Tab 1",
				Props: map[string]string{"application.process.id": "1000"}, Channels: stereo(1.0),
			},
			{
				ID: 11, Class: pw.Playback, Name: "chrome2", Client: "Google Chrome", Title: "Google Chrome: Tab 2",
				Props: map[string]string{"application.process.id": "1000"}, Channels: stereo(0.8),
			},
		},
	}
	m := newMixer(pw.NewClient(nil), nil, nil, quitKeys(), defaultOptions())
	m.snap = snap
	m.mediaTitles = map[uint32]string{10: "When the Sun Hits", 11: "When the Sun Hits"}
	m.setTab(tabPlayback)
	m.setSize(80, 20)
	m.refocus()

	items := m.items(tabPlayback)
	if len(items) != 3 || !items[1].groupLeader || items[2].groupLeader {
		t.Fatalf("items = %+v", items)
	}

	lines := strings.Split(ansi.Strip(m.view()), "\n")
	if !strings.Contains(lines[3], "Google Chrome: When the Sun Hits") ||
		!strings.Contains(lines[4], "┌") || !strings.Contains(lines[4], "FL+FR 100") ||
		!strings.Contains(lines[5], "└") || !strings.Contains(lines[5], "FL+FR  80") {
		t.Errorf("grouped view:\n%s", strings.Join(lines[:6], "\n"))
	}
	for _, row := range []int{2, 4, 5} {
		barIdx := strings.Index(lines[row], faderFull)
		if col := ansi.StringWidth(lines[row][:barIdx]); col != barX {
			t.Errorf("row %d: bar at column %d, want %d", row, col, barX)
		}
	}

	_, i, line, ok := m.hit(4)
	if !ok || items[i].id != 10 || !onStreamMuteButton(items[i], line, streamMuteX) {
		t.Errorf("row 4 hit: item %d, ok %v", items[i].id, ok)
	}

	// Unequal channel volumes render an inner channel bracket under the group stem.
	snap.Nodes[1].Channels = []pw.Channel{{Name: "FL", Volume: 1.0}, {Name: "FR", Volume: 0.9}}
	m.refocus()
	lines = strings.Split(ansi.Strip(m.view()), "\n")
	if !strings.Contains(lines[5], "│") || !strings.Contains(lines[5], "FR  90") {
		t.Errorf("unfolded group member:\n%s", strings.Join(lines[:7], "\n"))
	}
}

func TestToggleSolo(t *testing.T) {
	m := testMixer()
	findNode := func(id uint32) *pw.Node {
		for i := range m.snap.Nodes {
			if m.snap.Nodes[i].ID == id {
				return &m.snap.Nodes[i]
			}
		}
		return nil
	}

	m.handleKey("s")
	if m.soloID != 10 || findNode(10).Mute || !findNode(11).Mute || !findNode(12).Mute || findNode(20).Mute {
		t.Fatalf("after solo 10: soloID=%d, mutes 10=%v 11=%v 12=%v 20=%v",
			m.soloID, findNode(10).Mute, findNode(11).Mute, findNode(12).Mute, findNode(20).Mute)
	}
	if status := ansi.Strip(m.statusLine()); !strings.Contains(status, "[SOLO] Player A") {
		t.Errorf("status = %q", status)
	}

	m.handleKey("j")
	m.handleKey("s")
	if m.soloID != 11 || !findNode(10).Mute || findNode(11).Mute {
		t.Fatalf("after transfer to 11: soloID=%d, mutes 10=%v 11=%v", m.soloID, findNode(10).Mute, findNode(11).Mute)
	}

	if cmd := m.handleKey("s"); cmd != nil {
		m.update(cmd())
	}
	if m.soloID != 0 || findNode(10).Mute || findNode(11).Mute || !findNode(12).Mute {
		t.Fatalf("after solo off: soloID=%d, mutes 10=%v 11=%v 12=%v",
			m.soloID, findNode(10).Mute, findNode(11).Mute, findNode(12).Mute)
	}

	m.handleKey("s")
	m.snap.Nodes = slices.DeleteFunc(m.snap.Nodes, func(n pw.Node) bool { return n.ID == 11 })
	m.refocus()
	if m.soloID != 0 || findNode(10).Mute || !findNode(12).Mute {
		t.Errorf("after soloed stream removed: soloID=%d, mutes 10=%v 12=%v",
			m.soloID, findNode(10).Mute, findNode(12).Mute)
	}
}
