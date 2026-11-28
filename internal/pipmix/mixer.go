package pipmix

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/some-programs/pipmix/internal/mpris"
	"github.com/some-programs/pipmix/internal/pulse"
	"github.com/some-programs/pipmix/internal/pw"
	"github.com/some-programs/pipmix/internal/tui"
)

const noticeTime = 5 * time.Second

type tab int

const (
	tabDefault tab = iota
	tabPlayback
	tabRecording
	tabOutputs
	tabInputs
	tabCards
	tabCount
)

var tabNames = [tabCount]string{"Default", "Playback", "Recording", "Output Devices", "Input Devices", "Cards"}

var tabClasses = [tabCards][]pw.Class{{pw.Sink, pw.Playback, pw.Recording, pw.Source}, {pw.Playback}, {pw.Recording}, {pw.Sink}, {pw.Source}}

type item struct {
	id     uint32
	node   *pw.Node
	card   *pw.Card
	media  string
	player *mpris.Player

	groupKey    string
	groupTitle  string
	groupLeader bool
	groupLast   bool
	groupCount  int
	groupIndex  int
}

func (it item) channels() int {
	if it.node == nil {
		return 0
	}
	return len(it.node.Channels)
}

func (it item) title() string {
	switch {
	case it.card != nil:
		return it.card.Title
	case it.node.JACK:
		return "JACK: " + it.node.Title
	case it.media != "":
		return it.node.Client + ": " + it.media
	}
	return it.node.Title
}

type focus struct {
	id      uint32
	index   int // fallback position when id is removed
	channel int
	chosen  bool // set once the user moves focus
}

type menuKind int

const (
	menuRoute menuKind = iota
	menuProfile
)

type menu struct {
	kind   menuKind
	target uint32
	title  string
	items  []menuItem
	cursor int
}

type menuItem struct {
	label  string
	index  int32
	active bool
	dim    bool
}

type drag struct {
	node    uint32
	channel int
	last    float64
	sent    float64
	sentAt  time.Time
}

type nodePID struct {
	id  uint32
	pid string
}

type mixer struct {
	client       *pw.Client
	snap         *pw.Snapshot
	media        *mpris.Watcher
	mediaSnap    *mpris.Snapshot // last MPRIS snapshot matched by findMedia
	mediaNodes   []nodePID       // last playback nodes matched by findMedia
	mediaTitles  map[uint32]string
	mediaPlayers map[uint32]mpris.Player
	meter        *pulse.Meter
	watched      map[uint32]pulse.Target
	fill         map[uint32][]float64
	fillStart    map[uint32]time.Time
	dot          map[uint32][]float64
	dotHold      map[uint32][]time.Time
	levelsAt     time.Time
	levelsSnap   *pulse.Snapshot // last meter snapshot pruned against
	levelsPWSnap *pw.Snapshot    // last PipeWire snapshot pruned against
	levelsMode   meterMode       // last meterMode pruned against
	meterRows    []meterRow      // where the last draw put meters
	ticking      bool
	meterWaiting bool // a waitMeter command is pending
	blurred      bool
	smoothing    int
	metersHidden bool
	meterMode    meterMode
	quitKeys     section
	opts         options
	hide         []hideRule // [[default.hide]] rules
	act          activity
	redraw       bool // the screen needs drawing for a change made outside update

	tab      tab
	focus    [tabCount]focus
	offset   [tabCount]int
	unlocked map[string]bool
	menu     *menu
	help     bool
	helpOff  int
	drag     *drag

	notice    string
	noticeErr bool
	noticeSeq int

	soloID    uint32
	soloSaved map[uint32]bool
	soloTitle string

	width, height int
}

func newMixer(c *pw.Client, media *mpris.Watcher, meter *pulse.Meter, quitKeys section, opts options) mixer {
	if opts.meters == metersOff {
		meter = nil
	}
	return mixer{
		client: c, snap: c.Snapshot(), media: media, meter: meter, quitKeys: quitKeys, opts: opts,
		tab: opts.tab, meterMode: opts.meterMode, smoothing: opts.smoothing,
		unlocked: make(map[string]bool),
		// init starts the first wait, on a copy of the mixer.
		meterWaiting: meter != nil,
	}
}

type (
	snapshotMsg struct{}
	mediaMsg    struct{}
)

type noticeMsg struct {
	text string
	err  bool
}

type noticeExpiredMsg int

func notify(text string) command {
	return func() message { return noticeMsg{text: text} }
}

func (m *mixer) waitSnapshot() command {
	updated := m.client.Updated()
	return func() message {
		<-updated
		return snapshotMsg{}
	}
}

func (m *mixer) waitMedia() command {
	if m.media == nil {
		return nil
	}
	updated := m.media.Updated()
	return func() message {
		<-updated
		return mediaMsg{}
	}
}

func (m *mixer) init() []command { return []command{m.waitSnapshot(), m.waitMedia(), m.nextMeter()} }

// findMedia matches playback streams to MPRIS players by process ID.
func (m *mixer) findMedia() {
	if m.media == nil {
		m.mediaTitles = nil
		m.mediaPlayers = nil
		m.mediaSnap = nil
		m.mediaNodes = nil
		return
	}
	ms := m.media.Snapshot()
	if ms == m.mediaSnap && m.sameMediaNodes() {
		return
	}
	m.mediaSnap = ms
	m.mediaNodes = m.mediaNodes[:0]
	for _, n := range m.snap.Nodes {
		if n.Class != pw.Playback || n.JACK {
			continue
		}
		m.mediaNodes = append(m.mediaNodes, nodePID{id: n.ID, pid: n.Props["application.process.id"]})
	}
	m.mediaTitles, m.mediaPlayers = streamMedia(m.snap.Nodes, ms)
}

func (m *mixer) sameMediaNodes() bool {
	k := 0
	for _, n := range m.snap.Nodes {
		if n.Class != pw.Playback || n.JACK {
			continue
		}
		if k >= len(m.mediaNodes) || m.mediaNodes[k].id != n.ID || m.mediaNodes[k].pid != n.Props["application.process.id"] {
			return false
		}
		k++
	}
	return k == len(m.mediaNodes)
}

func streamMedia(nodes []pw.Node, players *mpris.Snapshot) (map[uint32]string, map[uint32]mpris.Player) {
	var titles map[uint32]string
	var matched map[uint32]mpris.Player
	for _, n := range nodes {
		if n.Class != pw.Playback || n.JACK {
			continue
		}
		pid, err := strconv.Atoi(n.Props["application.process.id"])
		if err != nil {
			continue
		}
		if p, ok := players.Player(pid); ok {
			if matched == nil {
				matched = make(map[uint32]mpris.Player)
			}
			matched[n.ID] = p
			if p.Title != "" {
				if titles == nil {
					titles = make(map[uint32]string)
				}
				titles[n.ID] = p.Title
			}
		}
	}
	return titles, matched
}

func (m *mixer) setSize(width, height int) {
	m.width, m.height = width, height
	m.scroll()
	m.scrollHelp(0)
}

func (m *mixer) update(msg message) command {
	switch msg := msg.(type) {
	case snapshotMsg:
		m.snap = m.client.Snapshot()
		m.findMedia()
		m.updateShown(time.Now())
		items := m.refocus()
		m.watchMetersItems(items)
		return m.waitSnapshot()
	case mediaMsg:
		m.findMedia()
		m.watchMeters()
		return m.waitMedia()
	case meterMsg:
		m.meterWaiting = false
		return m.startMetering()
	case tui.FocusEvent:
		m.blurred = false
		m.watchMeters()
		return m.startMetering()
	case tui.BlurEvent:
		m.blurred = true
		m.watchMeters()
		return m.startMetering()
	case noticeMsg:
		m.notice, m.noticeErr = msg.text, msg.err
		m.noticeSeq++
		seq := m.noticeSeq
		m.scroll()
		m.scrollHelp(0)
		return after(noticeTime, noticeExpiredMsg(seq))
	case noticeExpiredMsg:
		if int(msg) == m.noticeSeq {
			m.notice = ""
			m.scroll()
			m.scrollHelp(0)
		}
	}
	return nil
}

// items returns the items for tab t, with the default device first and JACK
// clients last. The Default tab lists other output devices below those,
// then input devices, then recording streams.
func (m *mixer) items(t tab) []item {
	var items, fixed, outputs, inputs, recording, hidden []item
	if t == tabCards {
		for i := range m.snap.Cards {
			c := &m.snap.Cards[i]
			items = append(items, item{id: c.ID, card: c})
		}
		return items
	}
	for i := range m.snap.Nodes {
		n := &m.snap.Nodes[i]
		if !m.listed(t, n) {
			// A stream that a rule hides still has its duplicates dropped.
			if t == tabDefault && n.Class == pw.Playback && m.act.hidden[n.ID] && m.listed(tabPlayback, n) {
				hidden = append(hidden, item{id: n.ID, node: n, media: m.mediaTitles[n.ID]})
			}
			continue
		}
		var player *mpris.Player
		if p, ok := m.mediaPlayers[n.ID]; ok {
			player = &p
		}
		it := item{id: n.ID, node: n, media: m.mediaTitles[n.ID], player: player}
		switch {
		case t == tabDefault && n.Class == pw.Sink && !n.Default:
			outputs = append(outputs, it)
		case t == tabDefault && n.Class == pw.Source:
			if n.Default {
				inputs = slices.Insert(inputs, 0, it)
			} else {
				inputs = append(inputs, it)
			}
		case t == tabDefault && n.Class == pw.Recording:
			recording = append(recording, it)
		case n.JACK:
			fixed = append(fixed, it)
		case n.Default:
			items = slices.Insert(items, 0, it)
		default:
			items = append(items, it)
		}
	}
	all := append(items, fixed...)
	if t == tabRecording {
		return groupStreams(all)
	}
	if t != tabDefault && t != tabPlayback {
		return all
	}
	if m.opts.dedup {
		among := all
		if len(hidden) > 0 {
			among = slices.Concat(all, hidden)
		}
		all = filterDuplicates(all, among)
	}
	all = groupStreams(all)
	if t != tabDefault {
		return all
	}
	return slices.Concat(all, outputs, inputs, groupStreams(recording))
}

// listed reports whether tab t lists node n.
func (m *mixer) listed(t tab, n *pw.Node) bool {
	if !slices.Contains(tabClasses[t], n.Class) || n.JACK && !m.opts.jack {
		return false
	}
	if t != tabDefault {
		return true
	}
	if !listable(n) || m.act.hidden[n.ID] {
		return false
	}
	switch m.defaultShow(n) {
	case showAll:
		return true
	case showActive:
		return m.act.shown[n.ID]
	}
	return false
}

// streamGroupKey returns a key that is equal for streams that belong in
// the same group: playback streams of one process that play to the same
// nodes, or recording streams of one process that record from the same
// nodes. It returns "" if it is not a stream or can't be grouped.
func streamGroupKey(it item) string {
	n := it.node
	if n == nil || n.JACK || n.Class != pw.Playback && n.Class != pw.Recording {
		return ""
	}
	pid := n.Props["application.process.id"]
	if pid == "" {
		return ""
	}
	return fmt.Sprintf("%d %s %v %v", n.Class, pid, n.From, n.To)
}

// groupStreams groups multiple playback or recording streams from the same
// application so they appear together under a single application header.
func groupStreams(items []item) []item {
	keys := make([]string, len(items))
	counts := make(map[string]int)
	grouped := false
	for i, it := range items {
		if k := streamGroupKey(it); k != "" {
			keys[i] = k
			counts[k]++
			grouped = grouped || counts[k] > 1
		}
	}
	if !grouped {
		return items
	}

	groupItems := make(map[string][]item)
	for i, it := range items {
		if k := keys[i]; counts[k] > 1 {
			groupItems[k] = append(groupItems[k], it)
		}
	}

	var res []item
	placed := make(map[string]bool)
	for i, it := range items {
		k := keys[i]
		if counts[k] <= 1 {
			res = append(res, it)
			continue
		}
		if placed[k] {
			continue
		}
		placed[k] = true
		grp := groupItems[k]

		hasDistinctMedia := false
		var firstMedia string
		for _, member := range grp {
			mTitle := member.media
			if mTitle == "" && member.node != nil && member.node.Props["media.name"] != "" && member.node.Props["media.name"] != "Playback" {
				mTitle = member.node.Props["media.name"]
			}
			if mTitle != "" {
				if firstMedia == "" {
					firstMedia = mTitle
				} else if firstMedia != mTitle {
					hasDistinctMedia = true
					break
				}
			}
		}
		if hasDistinctMedia {
			res = append(res, grp...)
			continue
		}

		title := ""
		var player *mpris.Player
		for _, member := range grp {
			if member.media != "" && title == "" {
				if member.node != nil && member.node.Client != "" {
					title = member.node.Client + ": " + member.media
				} else {
					title = member.media
				}
			}
			if member.player != nil && player == nil {
				player = member.player
			}
		}
		if title == "" && len(grp) > 0 && grp[0].node != nil {
			if grp[0].node.Client != "" {
				title = grp[0].node.Client
			} else {
				title = grp[0].node.Title
			}
		}

		for idx, member := range grp {
			member.groupKey = k
			member.groupTitle = title
			if player != nil && member.player == nil {
				member.player = player
			}
			member.groupCount = len(grp)
			member.groupIndex = idx
			member.groupLeader = (idx == 0)
			member.groupLast = (idx == len(grp)-1)
			res = append(res, member)
		}
	}
	return res
}

func (m *mixer) toggleGroupMute(groupKey string) {
	if groupKey == "" {
		return
	}
	items := m.items(m.tab)
	anyUnmuted := false
	for _, it := range items {
		if it.groupKey == groupKey && it.node != nil && !it.node.Mute {
			anyUnmuted = true
			break
		}
	}
	for _, it := range items {
		if it.groupKey == groupKey && it.node != nil {
			if anyUnmuted && !it.node.Mute {
				m.client.ToggleMute(it.id)
			} else if !anyUnmuted && it.node.Mute {
				m.client.ToggleMute(it.id)
			}
		}
	}
}

// filterDuplicates returns items without the mono playback streams that
// duplicate a multi-channel stream in among, from the same application or
// media.
func filterDuplicates(items, among []item) []item {
	for i, it := range items {
		if !isDuplicate(it, among) {
			continue
		}
		filtered := make([]item, 0, len(items)-1)
		filtered = append(filtered, items[:i]...)
		for _, rest := range items[i+1:] {
			if !isDuplicate(rest, among) {
				filtered = append(filtered, rest)
			}
		}
		return filtered
	}
	return items
}

func isDuplicate(it item, items []item) bool {
	if it.node == nil || it.node.Class != pw.Playback || it.node.JACK || it.channels() != 1 {
		return false
	}
	pid := it.node.Props["application.process.id"]
	for _, other := range items {
		if other.id == it.id || other.node == nil || other.node.Class != pw.Playback || other.node.JACK || other.channels() <= 1 {
			continue
		}
		otherPid := other.node.Props["application.process.id"]
		sameProcess := pid != "" && pid == otherPid
		sameClient := it.node.Client != "" && it.node.Client == other.node.Client
		if !sameProcess && !sameClient {
			continue
		}
		if it.media != "" && other.media != "" && it.media != other.media {
			continue
		}
		return true
	}
	return false
}

// focused returns the focused item of the current tab and its position,
// or -1 if the tab is empty.
func (m *mixer) focused() (item, int) {
	items := m.items(m.tab)
	i := m.focusIndex(m.tab, items)
	if i < 0 {
		return item{}, -1
	}
	return items[i], i
}

// focusIndex finds tab t's focused item in items. If it's gone, the item
// now at its position takes over.
func (m *mixer) focusIndex(t tab, items []item) int {
	if len(items) == 0 {
		return -1
	}
	f := &m.focus[t]
	if f.chosen {
		for i, it := range items {
			if it.id == f.id {
				f.index = i
				return i
			}
		}
	}
	i := min(f.index, len(items)-1)
	if items[i].id != f.id {
		*f = focus{id: items[i].id, index: i, chosen: f.chosen}
	}
	return i
}

// setFocus focuses item i of the current tab, on channel if its channels
// are unlocked.
func (m *mixer) setFocus(items []item, i, channel int) {
	m.focus[m.tab] = focus{id: items[i].id, index: i, chosen: true}
	if m.isUnlocked(items[i]) {
		m.focus[m.tab].channel = max(0, min(channel, items[i].channels()-1))
	}
}

func (m *mixer) refocusTab(t tab, items []item) {
	if i := m.focusIndex(t, items); i >= 0 {
		f := &m.focus[t]
		f.channel = min(f.channel, max(items[i].channels()-1, 0))
	}
}

// refocus fixes up focus, menu, drag and scrolling after the items
// changed, and returns the current tab's items.
func (m *mixer) refocus() []item {
	items := m.items(m.tab)
	m.refocusTab(m.tab, items)
	if m.menu != nil && !m.exists(m.menu.target) {
		m.menu = nil
	}
	if m.drag != nil && !m.exists(m.drag.node) {
		m.drag = nil
	}
	if m.soloID != 0 {
		if !m.exists(m.soloID) {
			m.restoreSoloMutes()
			m.soloID = 0
			m.soloSaved = nil
			m.soloTitle = ""
		} else {
			pbItems := items
			if m.tab != tabPlayback {
				pbItems = m.items(tabPlayback)
			}
			for _, it := range pbItems {
				if it.id == m.soloID {
					m.soloTitle = it.title()
					break
				}
			}
			for i := range m.snap.Nodes {
				n := &m.snap.Nodes[i]
				if n.Class == pw.Playback && !n.JACK && n.ID != m.soloID {
					if _, ok := m.soloSaved[n.ID]; !ok {
						m.soloSaved[n.ID] = n.Mute
						n.Mute = true
						if m.client != nil {
							m.client.SetMute(n.ID, true)
						}
					}
				}
			}
		}
	}
	m.scrollItems(items)
	return items
}

func (m *mixer) restoreSoloMutes() {
	if m.soloSaved == nil {
		return
	}
	for id, mute := range m.soloSaved {
		if m.client != nil {
			m.client.SetMute(id, mute)
		}
		for i := range m.snap.Nodes {
			if m.snap.Nodes[i].ID == id {
				m.snap.Nodes[i].Mute = mute
			}
		}
	}
}

func (m *mixer) exists(id uint32) bool {
	for _, n := range m.snap.Nodes {
		if n.ID == id {
			return true
		}
	}
	for _, c := range m.snap.Cards {
		if c.ID == id {
			return true
		}
	}
	return false
}

func lockKey(it item) string {
	if it.node != nil && it.node.Name != "" {
		return it.node.Name
	}
	return "#" + strconv.FormatUint(uint64(it.id), 10)
}

func (m *mixer) isUnlocked(it item) bool {
	return m.unlocked[lockKey(it)] && it.channels() > 1
}

// move moves focus by delta (-1 or 1) across unlocked channels and items.
func (m *mixer) move(delta int) {
	items := m.items(m.tab)
	i := m.focusIndex(m.tab, items)
	if i < 0 {
		return
	}
	f := &m.focus[m.tab]
	if m.isUnlocked(items[i]) {
		if c := f.channel + delta; c >= 0 && c < items[i].channels() {
			f.channel, f.chosen = c, true
			m.scroll()
			return
		}
	}
	j := i + delta
	if j < 0 || j >= len(items) {
		return
	}
	channel := 0
	if delta < 0 {
		channel = items[j].channels() - 1
	}
	m.setFocus(items, j, channel)
	m.scroll()
}

func (m *mixer) moveItems(n int) {
	items := m.items(m.tab)
	i := m.focusIndex(m.tab, items)
	if i < 0 {
		return
	}
	m.setFocus(items, max(0, min(i+n, len(items)-1)), 0)
	m.scroll()
}

func (m *mixer) pageItems() int {
	it, i := m.focused()
	if i < 0 {
		return 1
	}
	return max(1, m.listHeight()/m.itemHeight(it))
}

func (m *mixer) volumeTarget() (it item, channel int, cmd command) {
	it, i := m.focused()
	switch {
	case i < 0:
		return it, 0, notify("Nothing here")
	case it.node == nil:
		return it, 0, notify("Cards have no volume; their devices do")
	case it.node.JACK:
		return it, 0, notify(it.title() + ": PipeWire has no volume for JACK clients")
	case it.channels() == 0:
		return it, 0, notify("The volume of " + it.title() + " isn't known yet")
	}
	channel = -1
	if m.isUnlocked(it) {
		channel = m.focus[m.tab].channel
	}
	return it, channel, nil
}

func (m *mixer) changeVolume(delta float64) command {
	it, channel, cmd := m.volumeTarget()
	if cmd != nil {
		return cmd
	}
	m.client.ChangeVolume(it.id, channel, delta, m.opts.limit)
	return nil
}

func (m *mixer) setVolume100() command {
	it, channel, cmd := m.volumeTarget()
	if cmd != nil {
		return cmd
	}
	m.client.SetVolume(it.id, channel, 1.0, m.opts.limit)
	return nil
}

func (m *mixer) toggleMute() command {
	it, _, cmd := m.volumeTarget()
	if cmd != nil {
		return cmd
	}
	m.client.ToggleMute(it.id)
	return nil
}

func (m *mixer) toggleLock() command {
	it, i := m.focused()
	if i < 0 || it.channels() < 2 {
		return notify("Only items with more than one channel have a lock")
	}
	m.unlocked[lockKey(it)] = !m.unlocked[lockKey(it)]
	m.focus[m.tab].channel = 0
	m.scroll()
	return nil
}

func (m *mixer) setDefault() command {
	it, i := m.focused()
	if i < 0 || it.node == nil || (it.node.Class != pw.Sink && it.node.Class != pw.Source) {
		return notify("Only output and input devices can be the default")
	}
	m.client.SetDefault(it.id)
	return nil
}

func (m *mixer) openRouteMenu() command {
	it, i := m.focused()
	if i < 0 || it.node == nil || len(it.node.Routes) == 0 {
		return notify("Only devices of sound cards have ports")
	}
	if len(it.node.Routes) < 2 {
		return notify(it.node.Title + " has no other port")
	}
	mn := &menu{kind: menuRoute, target: it.id, title: "Port of " + it.node.Title}
	for j, r := range it.node.Routes {
		mn.items = append(mn.items, menuItem{label: r.Description, index: r.Index, active: j == it.node.Route, dim: !r.Available})
		if j == it.node.Route {
			mn.cursor = j
		}
	}
	m.menu = mn
	return nil
}

func (m *mixer) openProfileMenu() command {
	it, i := m.focused()
	if i < 0 || it.card == nil {
		return notify("Profiles are on the Cards tab")
	}
	if len(it.card.Profiles) < 2 {
		return notify(it.card.Title + " has no other profile")
	}
	mn := &menu{kind: menuProfile, target: it.id, title: "Profile of " + it.card.Title}
	for j, p := range it.card.Profiles {
		mn.items = append(mn.items, menuItem{label: p.Description, index: p.Index, active: j == it.card.Profile, dim: !p.Available})
		if j == it.card.Profile {
			mn.cursor = j
		}
	}
	m.menu = mn
	return nil
}

func (m *mixer) choose() {
	mn := m.menu
	it := mn.items[mn.cursor]
	if mn.kind == menuRoute {
		m.client.SetRoute(mn.target, it.index)
	} else {
		m.client.SetProfile(mn.target, it.index)
	}
	m.menu = nil
}

func (m *mixer) setTab(t tab) {
	m.tab = t
	m.flushDrag(true)
	m.drag = nil
	items := m.items(t)
	m.refocusTab(t, items)
	m.scrollItems(items)
}

func (m *mixer) togglePlayPause() command {
	it, i := m.focused()
	if i < 0 || it.node == nil || it.node.Class != pw.Playback {
		return notify("Only playback streams have media controls")
	}
	if it.player == nil || m.media == nil {
		return notify(it.title() + " has no media player")
	}
	name := it.player.Name
	media := m.media
	return func() message {
		if err := media.PlayPause(name); err != nil {
			return noticeMsg{text: "Play/pause: " + err.Error(), err: true}
		}
		return nil
	}
}

func (m *mixer) portOrPlayPause() command {
	it, i := m.focused()
	if i >= 0 && it.node != nil && it.node.Class == pw.Playback {
		return m.togglePlayPause()
	}
	return m.openRouteMenu()
}

// toggleSolo solos or unsolos the focused playback stream.
func (m *mixer) toggleSolo() command {
	it, i := m.focused()
	if i < 0 || it.node == nil || it.node.Class != pw.Playback || it.node.JACK {
		return notify("Only playback streams can be soloed")
	}

	if m.soloID == it.id {
		m.restoreSoloMutes()
		m.soloID = 0
		m.soloSaved = nil
		m.soloTitle = ""
		m.scroll()
		return notify("Solo off")
	}

	if m.soloID == 0 {
		m.soloSaved = make(map[uint32]bool)
		for _, n := range m.snap.Nodes {
			if n.Class == pw.Playback && !n.JACK {
				m.soloSaved[n.ID] = n.Mute
			}
		}
	} else if _, ok := m.soloSaved[it.id]; !ok {
		m.soloSaved[it.id] = it.node.Mute
	}

	for i := range m.snap.Nodes {
		n := &m.snap.Nodes[i]
		if n.Class == pw.Playback && !n.JACK {
			targetMute := (n.ID != it.id)
			n.Mute = targetMute
			if m.client != nil {
				m.client.SetMute(n.ID, targetMute)
			}
		}
	}

	m.soloID = it.id
	m.soloTitle = it.title()
	m.scroll()
	return nil
}
