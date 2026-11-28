package pw

import "strconv"

// A Snapshot is the mixer's state at one moment.
type Snapshot struct {
	Connected bool
	Err       error
	Nodes     []Node // ordered by ID
	Cards     []Card // ordered by ID
}

// A Class is the kind of audio node.
type Class int

const (
	Playback Class = iota
	Recording
	Sink
	Source
)

// A Node is an audio stream or device.
type Node struct {
	ID       uint32
	Class    Class
	Name     string
	Props    map[string]string
	Client   string
	Title    string
	JACK     bool
	Channels []Channel
	Mute     bool
	Default  bool
	State    State
	Routes   []Route
	Route    int // active index in Routes, or -1

	// Active reports whether the node processes audio for more than level
	// meters: a stream that runs, or a device that runs with a running
	// stream or other client linked to it.
	Active bool

	// From lists, in order, the nodes that link audio into a recording
	// stream, and To the nodes a playback stream links audio into, leaving
	// out level meters.
	From, To []uint32
}

// A State is a node's processing state.
type State int

const (
	StateError     State = -1
	StateCreating  State = 0
	StateSuspended State = 1
	StateIdle      State = 2
	StateRunning   State = 3
)

var stateNames = [...]string{"error", "creating", "suspended", "idle", "running"}

func (s State) String() string {
	if i := int(s) + 1; i >= 0 && i < len(stateNames) {
		return stateNames[i]
	}
	return "state(" + strconv.Itoa(int(s)) + ")"
}

// A Channel is one channel's volume on the cubic scale (1 is 100%).
type Channel struct {
	Name   string
	Volume float64
}

// A Route is a port of a sound card.
type Route struct {
	Index       int32
	Name        string
	Description string
	Available   bool
}

// A Card is an audio device and its profiles.
type Card struct {
	ID       uint32
	Title    string
	Profiles []Profile
	Profile  int // active index in Profiles, or -1
}

// A Profile is a configuration of a card.
type Profile struct {
	Index       int32
	Name        string
	Description string
	Available   bool
}
