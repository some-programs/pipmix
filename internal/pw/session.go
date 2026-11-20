package pw

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const pendingWindow = 500 * time.Millisecond

const (
	keyDefaultSink           = "default.audio.sink"
	keyDefaultSource         = "default.audio.source"
	keyConfiguredSink        = "default.configured.audio.sink"
	keyConfiguredSource      = "default.configured.audio.source"
	metadataTypeJSON         = "Spa:String:JSON"
	defaultMetadataName      = "default"
	mediaClassAudioDevice    = "Audio/Device"
	propDeviceID             = "device.id"
	propCardProfileDevice    = "card.profile.device"
	propNodeName             = "node.name"
	propMediaClass           = "media.class"
	propMetadataName         = "metadata.name"
	propNodeDescription      = "node.description"
	propMediaName            = "media.name"
	propDeviceDescription    = "device.description"
	propDeviceNick           = "device.nick"
	propDeviceName           = "device.name"
	propApplicationName      = "application.name"
	propApplicationProcessID = "application.process.id"
	propClientAPI            = "client.api"
	propMediaType            = "media.type"
	propStreamMonitor        = "stream.monitor"
)

// mediaClasses maps the node media classes pipmix shows to their Class.
var mediaClasses = map[string]Class{
	"Stream/Output/Audio": Playback,
	"Stream/Input/Audio":  Recording,
	"Audio/Sink":          Sink,
	"Audio/Source":        Source,
}

type proxyKind int

const (
	kindRegistry proxyKind = iota
	kindNode
	kindDevice
	kindMetadata
	kindLink
)

// A proxy is the client side of a bound object.
type proxy struct {
	id     uint32    // client-side proxy identifier
	kind   proxyKind // object type represented by this proxy
	global uint32    // PipeWire registry global ID
}

type node struct {
	id             uint32            // PipeWire registry global ID
	proxy          uint32            // client-side proxy ID
	class          Class             // stream/device media class
	props          map[string]string // node properties from registry
	viewProps      map[string]string // cached Props for snapshot (props merged with client fallback)
	viewPropsGen   uint64            // session.clientsGen when viewProps was built
	params         map[uint32]uint32 // param ID → flags from last Node::info event
	jack           bool              // true if JACK client without media class
	meter          bool              // true if peak meter stream node
	own            bool              // true if meter node owned by this instance
	inPorts        int32             // count of input ports
	outPorts       int32             // count of output ports
	known          bool              // true once volumes, channels, and mute are populated
	volumes        []float32         // current channel volumes (linear)
	channels       []uint32          // channel map positions (e.g. FL, FR)
	mute           bool              // true if node is muted
	pendingVolumes []float32         // last volume set by client while pending
	pendingMute    *bool             // last mute state set by client while pending
	pendingUntil   time.Time         // deadline until client pending values win
}

type device struct {
	id       uint32            // PipeWire registry global ID
	proxy    uint32            // client-side proxy ID
	props    map[string]string // device properties from registry
	params   map[uint32]uint32 // param ID → flags from last Device::info event
	profiles []Profile         // available card profiles from EnumProfile
	profile  int32             // active profile index, -1 if none
	routes   []routeInfo       // available routes from EnumRoute
	active   []activeRoute     // active card profile routes
}

// A routeInfo is an EnumRoute entry: a port the device can use.
type routeInfo struct {
	Route             // embedded public route metadata
	direction uint32  // SPA_DIRECTION_INPUT (0) or SPA_DIRECTION_OUTPUT (1)
	devices   []int32 // card profile devices this route applies to
	profiles  []int32 // profiles this route is available in
}

// An activeRoute is a Route entry: the port a card profile device uses.
type activeRoute struct {
	index  int32 // route index on the device
	device int32 // card profile device ID
}

// An enumReq collects the replies to one EnumParams.
type enumReq struct {
	seq    int32  // sequence number of EnumParams request
	proxy  uint32 // proxy ID targeted by the request
	global uint32 // registry global ID of the object
	param  uint32 // parameter ID being enumerated
	values []pod  // collected reply pods
}

type portInfo struct {
	id     uint32 // PipeWire registry global ID of the port
	node   uint32 // node ID this port belongs to
	portID int32  // port.id property identifying channel index
	name   string // port.name property (e.g. "playback_FL")
	out    bool   // true for output ports, false for input
}

// A jackMeter tracks a pipewire-pulse peak-detect stream for a JACK client.
type jackMeter struct {
	target   uint32        // target JACK node ID
	node     uint32        // meter stream's PipeWire node ID (0 until announced)
	ready    bool          // true once pipewire-pulse replied to stream creation
	linked   int           // count of port pairs linked so far
	unwanted []uint32      // default monitor link IDs queued for removal
	done     chan struct{} // channel closed when links are established
	syncSeq  int32         // sequence number of core sync awaiting completion
}

// A session is one connection to PipeWire and everything known through it.
// All of its methods run on the client's loop goroutine.
type session struct {
	conn        *conn                        // low-level PipeWire connection
	log         *log.Logger                  // logger for protocol diagnostics
	ids         idMap                        // allocator for client-side proxy IDs
	registry    uint32                       // proxy ID of the PipeWire registry
	proxies     map[uint32]*proxy            // proxy ID → proxy object
	nodes       map[uint32]*node             // global ID → audio node
	ports       map[uint32]portInfo          // global ID → audio port info
	jackMeters  map[string]*jackMeter        // meter node name → JACK meter tracker
	meters      map[uint32]*node             // global ID → meter stream nodes owned by this instance
	meterPrefix string                       // prefix of meter stream nodes owned by this instance
	devices     map[uint32]*device           // global ID → audio device/card
	clients     map[uint32]map[string]string // global ID → client properties
	clientsGen  uint64                       // incremented whenever clients changes
	meta        *proxy                       // default metadata proxy, nil if not bound
	defaults    map[string]string            // default metadata key → node name
	seq         int32                        // next request sequence number
	enums       map[int32]*enumReq           // active EnumParams requests by sequence number
	syncs       map[int32][]*enumReq         // queued EnumParams commits waiting for core sync
	gen         uint64                       // registry generation received from server
	sentGen     uint64                       // registry generation acknowledged back to server
	readySeq    int32                        // sequence number of initial sync marking session ready
	ready       bool                         // true once initial sync completes
	dirty       bool                         // true if snapshot needs recomputing
	err         error                        // first fatal error ending the session
}

func newSession(c *conn, logger *log.Logger, pid int) *session {
	if pid == 0 {
		pid = os.Getpid()
	}
	return &session{
		conn:        c,
		log:         logger,
		ids:         idMap{next: idClient + 1},
		proxies:     make(map[uint32]*proxy),
		nodes:       make(map[uint32]*node),
		ports:       make(map[uint32]portInfo),
		jackMeters:  make(map[string]*jackMeter),
		meters:      make(map[uint32]*node),
		meterPrefix: fmt.Sprintf("%s%d.", MeterPrefix, pid),
		devices:     make(map[uint32]*device),
		clients:     make(map[uint32]map[string]string),
		clientsGen:  1,
		defaults:    make(map[string]string),
		enums:       make(map[int32]*enumReq),
		syncs:       make(map[int32][]*enumReq),
	}
}

// start sends the handshake and asks for the registry.
func (s *session) start() {
	s.send(idCore, coreHello, func(b *builder) {
		b.structure(func() { b.int(versionCore) })
	})
	s.send(idClient, clientUpdateProperties, func(b *builder) {
		b.structure(func() { b.dict(clientProps()) })
	})
	s.registry = s.ids.alloc()
	s.proxies[s.registry] = &proxy{id: s.registry, kind: kindRegistry}
	s.send(idCore, coreGetRegistry, func(b *builder) {
		b.structure(func() {
			b.int(versionRegistry)
			b.int(int32(s.registry))
		})
	})
	s.readySeq = s.sync()
}

func clientProps() [][2]string {
	kv := [][2]string{
		{propApplicationName, "pipmix"},
		{propApplicationProcessID, strconv.Itoa(os.Getpid())},
	}
	if exe, err := os.Executable(); err == nil {
		kv = append(kv, [2]string{"application.process.binary", filepath.Base(exe)})
	}
	return kv
}

// send writes a message to proxy id, its payload written by fill. Errors
// are kept in s.err, which ends the session.
func (s *session) send(id uint32, opcode uint8, fill func(*builder)) {
	if s.err != nil {
		return
	}
	var b builder
	fill(&b)
	var footer []byte
	if s.gen != s.sentGen {
		var f builder
		f.structure(func() {
			f.id(0)
			f.structure(func() { f.long(int64(s.gen)) })
		})
		footer = f.buf
		s.sentGen = s.gen
	}
	if err := s.conn.write(id, opcode, b.buf, footer); err != nil {
		s.err = fmt.Errorf("write: %w", err)
	}
}

func (s *session) nextSeq() int32 {
	s.seq = (s.seq + 1) & (asyncBit - 1)
	return s.seq | asyncBit
}

// sync sends Core::Sync and returns its seq, which Done will carry.
func (s *session) sync() int32 {
	seq := s.nextSeq()
	s.send(idCore, coreSync, func(b *builder) {
		b.structure(func() {
			b.int(idCore)
			b.int(seq)
		})
	})
	return seq
}

// enumerate reads params of a bound object. The replies are committed
// together when the Sync sent after them is done.
func (s *session) enumerate(p *proxy, params []uint32) {
	if len(params) == 0 {
		return
	}
	var reqs []*enumReq
	for _, id := range params {
		seq := s.nextSeq()
		r := &enumReq{seq: seq, proxy: p.id, global: p.global, param: id}
		s.enums[seq] = r
		reqs = append(reqs, r)
		s.send(p.id, methodEnumParams, func(b *builder) {
			b.structure(func() {
				b.int(seq)
				b.id(id)
				b.int(0)
				b.int(-1)
				b.none()
			})
		})
	}
	s.syncs[s.sync()] = reqs
}

// handle processes one message from the server.
func (s *session) handle(m message) error {
	s.footer(m.footer)
	f, err := m.payload.fields()
	if err != nil {
		return fmt.Errorf("message %d/%d: %w", m.id, m.opcode, err)
	}
	if m.id == idCore {
		return s.coreEvent(m.opcode, f)
	}
	p := s.proxies[m.id]
	if p == nil {
		return nil
	}
	switch p.kind {
	case kindRegistry:
		return s.registryEvent(m.opcode, f)
	case kindNode:
		return s.nodeEvent(p, m.opcode, f)
	case kindDevice:
		return s.deviceEvent(p, m.opcode, f)
	case kindMetadata:
		return s.metadataEvent(m.opcode, f)
	}
	return nil
}

// footer reads a message footer: Struct((Id opcode, Struct args)*).
func (s *session) footer(p pod) {
	if p.typ != typeStruct {
		return
	}
	f, err := p.fields()
	if err != nil {
		return
	}
	for f.more() {
		op := f.u32()
		args := f.fields()
		if op == 0 { // registry generation
			if gen := uint64(args.long()); args.err == nil && gen > s.gen {
				s.gen = gen
			}
		}
	}
}

func (s *session) coreEvent(op uint8, f *parser) error {
	switch op {
	case coreEventDone:
		f.int()
		seq := f.int()
		if f.err != nil {
			return f.err
		}
		if reqs, ok := s.syncs[seq]; ok {
			delete(s.syncs, seq)
			for _, r := range reqs {
				s.commit(r)
			}
		}
		for _, jm := range s.jackMeters {
			if jm.done != nil && jm.syncSeq == seq {
				close(jm.done)
				jm.done = nil
			}
		}
		if seq == s.readySeq && !s.ready {
			s.ready = true
			s.dirty = true
		}
	case coreEventPing:
		id := f.int()
		seq := f.int()
		if f.err != nil {
			return f.err
		}
		s.send(idCore, corePong, func(b *builder) {
			b.structure(func() {
				b.int(id)
				b.int(seq)
			})
		})
	case coreEventError:
		id := f.u32()
		seq := f.int()
		res := f.int()
		msg := f.string()
		s.log.Printf("pipewire: error on object %d (seq %d): %d %s", id, seq, res, msg)
	case coreEventRemoveID:
		id := f.u32()
		if f.err != nil {
			return f.err
		}
		s.removeProxy(id)
	}
	return f.err
}

func (s *session) removeProxy(id uint32) {
	p := s.proxies[id]
	if p == nil {
		return
	}
	delete(s.proxies, id)
	s.ids.release(id)
	if p.kind == kindLink {
		return
	}
	switch {
	case p.kind == kindNode && s.nodes[p.global] != nil && s.nodes[p.global].proxy == id:
		delete(s.nodes, p.global)
		delete(s.meters, p.global)
	case p.kind == kindDevice && s.devices[p.global] != nil && s.devices[p.global].proxy == id:
		delete(s.devices, p.global)
	case p.kind == kindMetadata && s.meta == p:
		s.meta = nil
		clear(s.defaults)
	}
	s.dirty = true
}

func (s *session) registryEvent(op uint8, f *parser) error {
	switch op {
	case registryEventGlobal:
		id := f.u32()
		f.u32() // permissions
		typ := f.string()
		version := f.int()
		props := f.dict()
		if f.err != nil {
			return f.err
		}
		s.global(id, typ, version, props)
	case registryEventGlobalRemove:
		id := f.u32()
		if f.err != nil {
			return f.err
		}
		if p, ok := s.ports[id]; ok {
			delete(s.ports, id)
			if n := s.nodes[p.node]; n != nil && n.jack {
				s.dirty = true
			}
			return nil
		}
		delete(s.nodes, id)
		delete(s.meters, id)
		for name, jm := range s.jackMeters {
			if jm.node == id {
				if jm.done != nil {
					close(jm.done)
					jm.done = nil
				}
				delete(s.jackMeters, name)
				break
			}
		}
		delete(s.devices, id)
		if _, ok := s.clients[id]; ok {
			delete(s.clients, id)
			s.clientsGen++
		}
		if s.meta != nil && s.meta.global == id {
			s.meta = nil
			clear(s.defaults)
		}
		s.dirty = true
	}
	return f.err
}

// global binds the globals pipmix shows or needs.
func (s *session) global(id uint32, typ string, version int32, props map[string]string) {
	switch typ {
	case ifaceClient:
		s.clients[id] = props
		s.clientsGen++
	case ifaceNode:
		name := props[propNodeName]
		meter := strings.HasPrefix(name, MeterPrefix)
		class, ok := mediaClasses[props[propMediaClass]]
		jack := props[propMediaClass] == "" && props[propClientAPI] == "jack" && props[propMediaType] == "Audio"
		if !ok && !jack && !meter {
			return
		}
		own := strings.HasPrefix(name, s.meterPrefix)
		p := s.bind(id, typ, min(version, versionNode), kindNode)
		n := &node{id: id, proxy: p.id, class: class, props: props, jack: jack, meter: meter, own: own}
		s.nodes[id] = n
		if meter {
			if own {
				s.meters[id] = n
				if target, ok := parseJackMeterTarget(name); ok {
					jm := s.ensureJackMeter(name, target)
					jm.node = id
					s.syncJackMeter(jm)
				}
			}
			return
		}
	case ifacePort:
		if props["port.monitor"] == "true" {
			return
		}
		if dsp := props["format.dsp"]; dsp != "" && !strings.Contains(dsp, "audio") {
			return
		}
		dir := props["port.direction"]
		if dir != "in" && dir != "out" {
			return
		}
		nodeID, err1 := strconv.ParseUint(props["node.id"], 10, 32)
		portID, err2 := strconv.ParseInt(props["port.id"], 10, 32)
		if err1 != nil || err2 != nil {
			return
		}
		nid := uint32(nodeID)
		s.ports[id] = portInfo{id: id, node: nid, portID: int32(portID), name: props["port.name"], out: dir == "out"}
		if n := s.nodes[nid]; n != nil && n.jack {
			s.dirty = true
		}
		for _, jm := range s.jackMeters {
			if jm.node == nid || jm.target == nid {
				s.syncJackMeter(jm)
			}
		}
		return
	case ifaceLink:
		inNode, err1 := strconv.ParseUint(props["link.input.node"], 10, 32)
		outNode, err2 := strconv.ParseUint(props["link.output.node"], 10, 32)
		if err1 != nil || err2 != nil {
			return
		}
		for _, jm := range s.jackMeters {
			if jm.node == uint32(inNode) && uint32(outNode) != jm.target {
				if jm.ready {
					s.destroyLink(id)
				} else {
					jm.unwanted = append(jm.unwanted, id)
				}
			}
		}
		return
	case ifaceDevice:
		if props[propMediaClass] != mediaClassAudioDevice {
			return
		}
		p := s.bind(id, typ, min(version, versionDevice), kindDevice)
		s.devices[id] = &device{id: id, proxy: p.id, props: props, profile: -1}
	case ifaceMetadata:
		if props[propMetadataName] != defaultMetadataName || s.meta != nil {
			return
		}
		s.meta = s.bind(id, typ, min(version, versionMetadata), kindMetadata)
	default:
		return
	}
	s.dirty = true
}

func parseJackMeterTarget(name string) (uint32, bool) {
	_, after, ok := strings.Cut(name, ".jack.")
	if !ok {
		return 0, false
	}
	idStr, _, _ := strings.Cut(after, ".")
	id, err := strconv.ParseUint(idStr, 10, 32)
	return uint32(id), err == nil
}

func (s *session) ensureJackMeter(name string, target uint32) *jackMeter {
	jm := s.jackMeters[name]
	if jm == nil {
		jm = &jackMeter{target: target}
		s.jackMeters[name] = jm
	}
	return jm
}

func (s *session) jackMeterReady(name string, done chan struct{}) {
	target, ok := parseJackMeterTarget(name)
	if !ok {
		close(done)
		return
	}
	jm := s.ensureJackMeter(name, target)
	jm.ready = true
	jm.done = done
	s.syncJackMeter(jm)
}

func (s *session) syncJackMeter(jm *jackMeter) {
	if !jm.ready || jm.node == 0 {
		return
	}
	outPorts := s.nodePorts(jm.target, true)
	inPorts := s.nodePorts(jm.node, false)
	for jm.linked < len(outPorts) && jm.linked < len(inPorts) {
		outPort := outPorts[jm.linked]
		inPort := inPorts[jm.linked]
		jm.linked++
		pid := s.ids.alloc()
		s.proxies[pid] = &proxy{id: pid, kind: kindLink}
		s.send(idCore, coreCreateObject, func(b *builder) {
			b.structure(func() {
				b.string("link-factory")
				b.string(ifaceLink)
				b.int(versionLink)
				b.dict([][2]string{
					{"link.output.port", strconv.FormatUint(uint64(outPort.id), 10)},
					{"link.input.port", strconv.FormatUint(uint64(inPort.id), 10)},
				})
				b.int(int32(pid))
			})
		})
	}
	for _, id := range jm.unwanted {
		s.destroyLink(id)
	}
	jm.unwanted = nil
	if jm.done != nil {
		jm.syncSeq = s.sync()
	}
}

func (s *session) destroyLink(id uint32) {
	s.send(s.registry, registryDestroy, func(b *builder) {
		b.structure(func() { b.int(int32(id)) })
	})
}

func (s *session) resetMeterVolume(n *node) {
	channels := max(1, len(n.volumes))
	vols := make([]float32, channels)
	for i := range vols {
		vols[i] = 1.0
	}
	s.setProps(n, func(b *builder) {
		b.prop(propChannelVolumes)
		b.floatArray(vols)
		b.prop(propMute)
		b.bool(false)
	})
}

func (s *session) nodePorts(nodeID uint32, out bool) []portInfo {
	var ports []portInfo
	for _, p := range s.ports {
		if p.node == nodeID && p.out == out {
			ports = append(ports, p)
		}
	}
	slices.SortFunc(ports, func(a, b portInfo) int {
		return cmp.Or(cmp.Compare(a.portID, b.portID), cmp.Compare(a.id, b.id))
	})
	return ports
}

func (s *session) bind(global uint32, typ string, version int32, kind proxyKind) *proxy {
	p := &proxy{id: s.ids.alloc(), kind: kind, global: global}
	s.proxies[p.id] = p
	s.send(s.registry, registryBind, func(b *builder) {
		b.structure(func() {
			b.int(int32(global))
			b.string(typ)
			b.int(version)
			b.int(int32(p.id))
		})
	})
	return p
}

func (s *session) nodeEvent(p *proxy, op uint8, f *parser) error {
	n := s.nodes[p.global]
	if n == nil || n.proxy != p.id {
		return nil
	}
	switch op {
	case eventInfo:
		f.int() // id
		f.int() // max input ports
		f.int() // max output ports
		change := f.long()
		inPorts := f.int()
		outPorts := f.int()
		f.u32() // state
		f.string()
		props := f.dict()
		params := f.paramInfo()
		if f.err != nil {
			return f.err
		}
		if inPorts != n.inPorts || outPorts != n.outPorts {
			n.inPorts, n.outPorts = inPorts, outPorts
			if n.jack {
				s.dirty = true
			}
		}
		if change&nodeChangeProps != 0 {
			n.props = props
			n.viewProps = nil
			n.viewPropsGen = 0
			if !n.meter {
				s.dirty = true
			}
		}
		if change&nodeChangeParams != 0 {
			s.enumerate(p, changedParams(n.params, params, paramProps))
			n.params = params
		}
	case eventParam:
		s.param(f)
	}
	return f.err
}

func (s *session) deviceEvent(p *proxy, op uint8, f *parser) error {
	d := s.devices[p.global]
	if d == nil || d.proxy != p.id {
		return nil
	}
	switch op {
	case eventInfo:
		f.int() // id
		change := f.long()
		props := f.dict()
		params := f.paramInfo()
		if f.err != nil {
			return f.err
		}
		if change&deviceChangeProps != 0 {
			d.props = props
			s.dirty = true
		}
		if change&deviceChangeParam != 0 {
			changed := changedParams(d.params, params, paramEnumProfile, paramProfile, paramEnumRoute, paramRoute)
			if len(changed) > 0 {
				// Profiles and routes refer to each other: read them all,
				// so they are committed as one consistent set.
				changed = []uint32{paramEnumProfile, paramProfile, paramEnumRoute, paramRoute}
			}
			s.enumerate(p, changed)
			d.params = params
		}
	case eventParam:
		s.param(f)
	}
	return f.err
}

// changedParams returns those of want that are readable and whose flags
// differ between the old and new param info.
func changedParams(old, new map[uint32]uint32, want ...uint32) []uint32 {
	var ids []uint32
	for _, id := range want {
		flags, ok := new[id]
		if !ok || flags&paramInfoRead == 0 {
			continue
		}
		if prev, seen := old[id]; seen && prev == flags {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// param stages one reply to an EnumParams.
func (s *session) param(f *parser) {
	seq := f.int()
	f.u32() // id
	f.int() // index
	f.int() // next
	v := f.pod()
	if f.err != nil {
		return
	}
	if r := s.enums[seq]; r != nil {
		r.values = append(r.values, v)
	}
}

// commit applies the replies to one EnumParams.
func (s *session) commit(r *enumReq) {
	delete(s.enums, r.seq)
	if n := s.nodes[r.global]; n != nil && n.proxy == r.proxy && r.param == paramProps {
		s.commitProps(n, r.values)
		return
	}
	d := s.devices[r.global]
	if d == nil || d.proxy != r.proxy {
		return
	}
	s.dirty = true
	switch r.param {
	case paramEnumProfile:
		d.profiles = nil
		for _, v := range r.values {
			if p, err := parseProfile(v); err == nil {
				d.profiles = append(d.profiles, p)
			}
		}
	case paramProfile:
		d.profile = -1
		for _, v := range r.values {
			if p, err := parseProfile(v); err == nil {
				d.profile = p.Index
			}
		}
	case paramEnumRoute:
		d.routes = nil
		for _, v := range r.values {
			if ri, err := parseRouteInfo(v); err == nil {
				d.routes = append(d.routes, ri)
			}
		}
	case paramRoute:
		d.active = nil
		for _, v := range r.values {
			o, err := v.object()
			if err != nil {
				continue
			}
			index, err1 := o.props[routeIndex].int()
			dev, err2 := o.props[routeDevice].int()
			if err1 == nil && err2 == nil {
				d.active = append(d.active, activeRoute{index: index, device: dev})
			}
		}
	}
}

func (s *session) commitProps(n *node, values []pod) {
	for _, v := range values {
		o, err := v.object()
		if err != nil {
			continue
		}
		vols, err := o.props[propChannelVolumes].floats()
		if err != nil {
			continue
		}
		n.volumes = vols
		n.channels, _ = o.props[propChannelMap].u32s()
		if mute, err := o.props[propMute].bool(); err == nil {
			n.mute = mute
		}
		n.known = true
		if n.meter {
			if n.own && (n.mute || slices.ContainsFunc(n.volumes, func(v float32) bool { return v != 1.0 })) {
				s.resetMeterVolume(n)
			}
			return
		}
		s.dirty = true
		return
	}
}

func parseProfile(v pod) (Profile, error) {
	o, err := v.object()
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if p.Index, err = o.props[profileIndex].int(); err != nil {
		return p, err
	}
	p.Name, _ = o.props[profileName].string()
	p.Description, _ = o.props[profileDescription].string()
	avail, err := o.props[profileAvailable].u32()
	p.Available = err != nil || avail != availableNo
	return p, nil
}

func parseRouteInfo(v pod) (routeInfo, error) {
	o, err := v.object()
	if err != nil {
		return routeInfo{}, err
	}
	var r routeInfo
	if r.Index, err = o.props[routeIndex].int(); err != nil {
		return r, err
	}
	if r.direction, err = o.props[routeDirection].u32(); err != nil {
		return r, err
	}
	r.Name, _ = o.props[routeName].string()
	r.Description, _ = o.props[routeDescription].string()
	avail, err := o.props[routeAvailable].u32()
	r.Available = err != nil || avail != availableNo
	r.devices, _ = o.props[routeDevices].ints()
	r.profiles, _ = o.props[routeProfiles].ints()
	return r, nil
}

func (s *session) metadataEvent(op uint8, f *parser) error {
	if op != metadataEventProperty {
		return nil
	}
	subject := f.u32()
	key := f.string()
	f.string() // type
	value := f.string()
	if f.err != nil {
		return f.err
	}
	if subject != 0 {
		return nil
	}
	s.dirty = true
	switch {
	case key == "":
		clear(s.defaults)
	case value == "":
		delete(s.defaults, key)
	default:
		var v struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			s.log.Printf("pipewire: metadata %s: %v", key, err)
			return nil
		}
		s.defaults[key] = v.Name
	}
	return nil
}

// current returns the node's volumes and mute as the user should see
// them: what the client set if that is recent, else what the server said.
func (n *node) current(now time.Time) (vols []float32, mute bool) {
	vols, mute = n.volumes, n.mute
	if now.Before(n.pendingUntil) {
		if n.pendingVolumes != nil {
			vols = n.pendingVolumes
		}
		if n.pendingMute != nil {
			mute = *n.pendingMute
		}
	}
	return vols, mute
}

// nextExpiry returns when the earliest pending value expires, or the zero
// time if none is pending.
func (s *session) nextExpiry(now time.Time) time.Time {
	var t time.Time
	for _, n := range s.nodes {
		if n.pendingUntil.After(now) && (t.IsZero() || n.pendingUntil.Before(t)) {
			t = n.pendingUntil
		}
	}
	return t
}

// deviceRoute returns the device behind a device node, its card profile
// device, and the index of its active route. ok is false if the node isn't
// a device node or its device or active route isn't known.
func (s *session) deviceRoute(n *node) (d *device, cpd int32, route int32, ok bool) {
	devID, err1 := strconv.ParseUint(n.props[propDeviceID], 10, 32)
	c, err2 := strconv.ParseInt(n.props[propCardProfileDevice], 10, 32)
	if err1 != nil || err2 != nil {
		return nil, 0, 0, false
	}
	d = s.devices[uint32(devID)]
	if d == nil {
		return nil, 0, 0, false
	}
	cpd = int32(c)
	for _, a := range d.active {
		if a.device == cpd {
			return d, cpd, a.index, true
		}
	}
	return d, cpd, -1, false
}

// setProps sets a node's Props, through its device's route if it has one.
func (s *session) setProps(n *node, fill func(*builder)) {
	props := func(b *builder) {
		b.object(objectProps, paramProps, func() { fill(b) })
	}
	d, cpd, route, ok := s.deviceRoute(n)
	if _, isDevice := n.props[propCardProfileDevice]; isDevice && !ok {
		s.log.Printf("pipewire: node %d: its device's active route isn't known yet", n.id)
		return
	}
	if ok {
		s.send(d.proxy, methodSetParam, func(b *builder) {
			b.structure(func() {
				b.id(paramRoute)
				b.int(0)
				b.object(objectParamRoute, paramRoute, func() {
					b.prop(routeIndex)
					b.int(route)
					b.prop(routeDevice)
					b.int(cpd)
					b.prop(routeProps)
					props(b)
					b.prop(routeSave)
					b.bool(true)
				})
			})
		})
		return
	}
	s.send(n.proxy, methodSetParam, func(b *builder) {
		b.structure(func() {
			b.id(paramProps)
			b.int(0)
			props(b)
		})
	})
}

func (s *session) changeVolume(id uint32, channel int, delta, limit float64) {
	s.updateVolumes(id, channel, limit, func(c float64) float64 { return step(c, delta) })
}

func (s *session) setVolume(id uint32, channel int, v, limit float64) {
	s.updateVolumes(id, channel, limit, func(float64) float64 { return v })
}

func (s *session) updateVolumes(id uint32, channel int, limit float64, f func(float64) float64) {
	n := s.nodes[id]
	if n == nil || !n.known {
		return
	}
	now := time.Now()
	cur, _ := n.current(now)
	vols := make([]float32, len(cur))
	changed := false
	for i, v := range cur {
		vols[i] = v
		if channel >= 0 && i != channel {
			continue
		}
		old := math.Cbrt(float64(v))
		c := max(0, min(f(old), max(limit, old)))
		vols[i] = float32(c * c * c)
		changed = changed || vols[i] != v
	}
	if !changed {
		return
	}
	s.setProps(n, func(b *builder) {
		b.prop(propChannelVolumes)
		b.floatArray(vols)
	})
	n.pendingVolumes = vols
	n.pendingUntil = now.Add(pendingWindow)
	s.dirty = true
}

// step moves volume c by delta to the next multiple of |delta| in delta's
// direction.
func step(c, delta float64) float64 {
	size := math.Abs(delta)
	if size == 0 {
		return c
	}
	const tolerance = 1e-3
	n := c / size
	if delta > 0 {
		n = math.Floor(n+tolerance) + 1
	} else {
		n = math.Ceil(n-tolerance) - 1
	}
	return math.Round(n*size*1e6) / 1e6
}

func (s *session) setMute(id uint32, mute bool) {
	n := s.nodes[id]
	if n == nil || !n.known {
		return
	}
	now := time.Now()
	_, currentMute := n.current(now)
	if currentMute == mute {
		return
	}
	s.setProps(n, func(b *builder) {
		b.prop(propMute)
		b.bool(mute)
	})
	n.pendingMute = &mute
	n.pendingUntil = now.Add(pendingWindow)
	s.dirty = true
}

func (s *session) toggleMute(id uint32) {
	n := s.nodes[id]
	if n == nil || !n.known {
		return
	}
	now := time.Now()
	_, mute := n.current(now)
	s.setMute(id, !mute)
}

// setRoute makes route index the active port of a device node.
func (s *session) setRoute(id uint32, index int32) {
	n := s.nodes[id]
	if n == nil {
		return
	}
	d, cpd, _, _ := s.deviceRoute(n)
	if d == nil {
		return
	}
	s.send(d.proxy, methodSetParam, func(b *builder) {
		b.structure(func() {
			b.id(paramRoute)
			b.int(0)
			b.object(objectParamRoute, paramRoute, func() {
				b.prop(routeIndex)
				b.int(index)
				b.prop(routeDevice)
				b.int(cpd)
				b.prop(routeSave)
				b.bool(true)
			})
		})
	})
}

func (s *session) setProfile(id uint32, index int32) {
	d := s.devices[id]
	if d == nil {
		return
	}
	s.send(d.proxy, methodSetParam, func(b *builder) {
		b.structure(func() {
			b.id(paramProfile)
			b.int(0)
			b.object(objectParamProfile, paramProfile, func() {
				b.prop(profileIndex)
				b.int(index)
				b.prop(profileSave)
				b.bool(true)
			})
		})
	})
}

// setDefault makes a sink or source the default one.
func (s *session) setDefault(id uint32) {
	n := s.nodes[id]
	if n == nil || s.meta == nil {
		return
	}
	var key, effective string
	switch n.class {
	case Sink:
		key, effective = keyConfiguredSink, keyDefaultSink
	case Source:
		key, effective = keyConfiguredSource, keyDefaultSource
	default:
		return
	}
	name := n.props[propNodeName]
	value, err := json.Marshal(struct {
		Name string `json:"name"`
	}{name})
	if err != nil {
		return
	}
	s.send(s.meta.id, metadataSetProperty, func(b *builder) {
		b.structure(func() {
			b.int(0)
			b.string(key)
			b.string(metadataTypeJSON)
			b.string(string(value))
		})
	})
	// The session manager will set the effective default; show it now.
	s.defaults[effective] = name
	s.dirty = true
}

func (s *session) snapshot(now time.Time) *Snapshot {
	snap := &Snapshot{Connected: true}
	for _, id := range slices.Sorted(maps.Keys(s.nodes)) {
		n := s.nodes[id]
		if n.meter || n.props[propStreamMonitor] == "true" {
			continue
		}
		if !n.jack {
			snap.Nodes = append(snap.Nodes, s.nodeView(n, now))
			continue
		}
		for _, c := range []struct {
			class Class
			ports int32
		}{{Playback, n.outPorts}, {Recording, n.inPorts}} {
			if c.ports > 0 {
				v := s.nodeView(n, now)
				v.Class, v.JACK = c.class, true
				if c.class == Playback {
					for i, p := range s.nodePorts(n.id, true) {
						name := cmp.Or(p.name, "CH"+strconv.Itoa(i+1))
						v.Channels = append(v.Channels, Channel{Name: name, Volume: 1})
					}
				}
				snap.Nodes = append(snap.Nodes, v)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.devices)) {
		snap.Cards = append(snap.Cards, cardView(s.devices[id]))
	}
	return snap
}

func (s *session) nodeView(n *node, now time.Time) Node {
	props := n.viewProps
	if n.viewPropsGen != s.clientsGen {
		props = n.props
		if clientID, err := strconv.ParseUint(n.props["client.id"], 10, 32); err == nil {
			if cp := s.clients[uint32(clientID)]; cp != nil {
				cloned := false
				for _, k := range []string{propApplicationProcessID, propApplicationName} {
					if _, ok := props[k]; !ok {
						if val := cp[k]; val != "" {
							if !cloned {
								props = maps.Clone(n.props)
								cloned = true
							}
							props[k] = val
						}
					}
				}
			}
		}
		n.viewProps = props
		n.viewPropsGen = s.clientsGen
	}
	v := Node{
		ID:    n.id,
		Class: n.class,
		Name:  n.props[propNodeName],
		Props: props,
		Route: -1,
	}
	v.Client = cmp.Or(props[propNodeDescription], v.Name, props[propApplicationName])
	v.Title = v.Client
	if media := props[propMediaName]; media != "" && media != v.Title {
		v.Title += ": " + media
	}
	if n.known {
		vols, mute := n.current(now)
		v.Mute = mute
		for i, vol := range vols {
			name := "CH" + strconv.Itoa(i+1)
			if len(n.channels) == len(vols) {
				name = channelName(n.channels[i])
			}
			v.Channels = append(v.Channels, Channel{Name: name, Volume: math.Cbrt(float64(vol))})
		}
	}
	switch n.class {
	case Sink:
		v.Default = v.Name != "" && s.defaults[keyDefaultSink] == v.Name
	case Source:
		v.Default = v.Name != "" && s.defaults[keyDefaultSource] == v.Name
	default:
		return v
	}
	d, cpd, active, _ := s.deviceRoute(n)
	if d == nil {
		return v
	}
	dir := uint32(directionOutput)
	if n.class == Source {
		dir = directionInput
	}
	for _, r := range d.routes {
		if r.direction != dir || !slices.Contains(r.devices, cpd) || !slices.Contains(r.profiles, d.profile) {
			continue
		}
		if r.Index == active {
			v.Route = len(v.Routes)
		}
		v.Routes = append(v.Routes, r.Route)
	}
	return v
}

func cardView(d *device) Card {
	c := Card{
		ID:       d.id,
		Title:    cmp.Or(d.props[propDeviceDescription], d.props[propDeviceNick], d.props[propDeviceName]),
		Profiles: d.profiles,
		Profile:  -1,
	}
	for i, p := range d.profiles {
		if p.Index == d.profile {
			c.Profile = i
		}
	}
	return c
}

// An idMap allocates proxy ids the way the server's map expects: an id
// freed by Core::RemoveId is reused first, last freed first, else the
// next new one.
type idMap struct {
	next uint32   // next unallocated proxy ID
	free []uint32 // pool of released proxy IDs available for reuse
}

func (m *idMap) alloc() uint32 {
	if n := len(m.free); n > 0 {
		id := m.free[n-1]
		m.free = m.free[:n-1]
		return id
	}
	id := m.next
	m.next++
	return id
}

func (m *idMap) release(id uint32) {
	if id <= idClient || id >= m.next || slices.Contains(m.free, id) {
		return
	}
	m.free = append(m.free, id)
}
