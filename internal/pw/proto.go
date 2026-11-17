package pw

import "strconv"

// Interface types, as announced by Registry::Global.
const (
	ifaceNode     = "PipeWire:Interface:Node"
	ifacePort     = "PipeWire:Interface:Port"
	ifaceLink     = "PipeWire:Interface:Link"
	ifaceDevice   = "PipeWire:Interface:Device"
	ifaceMetadata = "PipeWire:Interface:Metadata"
	ifaceClient   = "PipeWire:Interface:Client"
)

// Interface versions pipmix speaks.
const (
	versionCore     = 4
	versionRegistry = 3
	versionNode     = 3
	versionLink     = 3
	versionDevice   = 3
	versionMetadata = 3
)

// MeterPrefix prefixes the node.name of peak-detect streams created for
// sinks, sources, streams, and JACK clients.
const MeterPrefix = "pipmix.meter."

// Fixed proxy ids.
const (
	idCore   = 0
	idClient = 1
)

// Core methods and events.
const (
	coreHello        = 1
	coreSync         = 2
	corePong         = 3
	coreGetRegistry  = 5
	coreCreateObject = 6
	coreDestroy      = 7

	coreEventInfo     = 0
	coreEventDone     = 1
	coreEventPing     = 2
	coreEventError    = 3
	coreEventRemoveID = 4
)

const clientUpdateProperties = 2

const (
	registryBind    = 1
	registryDestroy = 2

	registryEventGlobal       = 0
	registryEventGlobalRemove = 1
)

// Node and Device share their method and event numbers.
const (
	methodEnumParams = 2
	methodSetParam   = 3

	eventInfo  = 0
	eventParam = 1
)

const (
	metadataSetProperty   = 1
	metadataEventProperty = 0
)

// Node::info and Device::info change mask bits.
const (
	nodeChangeProps   = 1 << 3
	nodeChangeParams  = 1 << 4
	deviceChangeProps = 1 << 0
	deviceChangeParam = 1 << 1
)

// paramInfoRead is SPA_PARAM_INFO_READ: the param can be enumerated.
const paramInfoRead = 1 << 1

// Param ids (enum spa_param_type).
const (
	paramProps       = 2
	paramEnumProfile = 8
	paramProfile     = 9
	paramEnumRoute   = 12
	paramRoute       = 13
)

// Object types.
const (
	objectProps        = 0x40002
	objectParamProfile = 0x40007
	objectParamRoute   = 0x40009
)

// Props keys (enum spa_prop).
const (
	propMute           = 0x10004
	propChannelVolumes = 0x10008
	propChannelMap     = 0x1000b
)

// Route keys (enum spa_param_route).
const (
	routeIndex       = 1
	routeDirection   = 2
	routeDevice      = 3
	routeName        = 4
	routeDescription = 5
	routePriority    = 6
	routeAvailable   = 7
	routeProfiles    = 9
	routeProps       = 10
	routeDevices     = 11
	routeProfile     = 12
	routeSave        = 13
)

// Profile keys (enum spa_param_profile).
const (
	profileIndex       = 1
	profileName        = 2
	profileDescription = 3
	profilePriority    = 4
	profileAvailable   = 5
	profileSave        = 8
)

// availableNo is SPA_PARAM_AVAILABILITY_no.
const availableNo = 1

// Directions (enum spa_direction), as in a route.
const (
	directionInput  = 0
	directionOutput = 1
)

// asyncBit marks a seq as an async result (SPA_RESULT_RETURN_ASYNC).
const asyncBit = 1 << 30

// channelNames are the short names of enum spa_audio_channel, by value.
var channelNames = []string{
	"UNK", "NA", "MONO", "FL", "FR", "FC", "LFE", "SL", "SR", "FLC", "FRC",
	"RC", "RL", "RR", "TC", "TFL", "TFC", "TFR", "TRL", "TRC", "TRR", "RLC",
	"RRC", "FLW", "FRW", "LFE2", "FLH", "FCH", "FRH", "TFLC", "TFRC", "TSL",
	"TSR", "LLFE", "RLFE", "BC", "BLC", "BRC",
}

// channelName returns the short name of channel position c.
func channelName(c uint32) string {
	switch {
	case int(c) < len(channelNames):
		return channelNames[c]
	case c >= 0x1000 && c <= 0x1fff:
		return "AUX" + strconv.Itoa(int(c-0x1000))
	}
	return "UNK"
}
