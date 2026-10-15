package decode

import "time"

const
// EventCPU is a thread sampled while on CPU.
EventCPU = "cpu"
const
// EventWall is a thread sampled whatever its state, as async-profiler's
// wall-clock mode samples them.
EventWall = "wall"
const
// EventAlloc is an allocation: a new TLAB, an object allocated outside
// one, or the JDK's throttled allocation sample.
EventAlloc = "alloc"
const
// EventLock is a thread blocked entering a contended monitor.
EventLock = "lock"

// The events a profile's samples are of.
const (
	// EventPark is a thread parked, as by LockSupport.park.
	EventPark = "park"
)
const UnitSamples = "samples"
const UnitBytes = "bytes"

// The units a sample's Value is in.
const (
	UnitNanos = "ns"
)

// StackSample is one stack's share of a dump: the samples of one event that
// ended on that stack, on one thread when a capture splits them by thread,
// within the window a dump added to the recording.
type StackSample struct {
	// Timestamp is the end of the dump's window: the latest event it added.
	Timestamp time.Time `json:"timestamp" pretty:"label=At" sort:"timestamp"`
	// From is the start of the dump's window: the earliest event it added.
	From   time.Time `json:"from" pretty:"hide"`
	Event  string    `json:"event" filter:"terms"`
	Thread string    `json:"thread,omitempty" filter:"terms"`
	// Frame is the stack's leaf, the frame the event happened in.
	Frame string `json:"frame" pretty:"label=Top frame" filter:"text"`
	// Stack is the collapsed stack, root first: frames joined by ";", headed
	// by "[truncated]" when the recorder or MaxStackDepth cut its root side.
	Stack   string `json:"stack" pretty:"hide" filter:"text"`
	Samples int64  `json:"samples" sort:"samples"`
	// Value is the samples' weight in Unit: their count for cpu and wall,
	// the bytes allocated for alloc, the time blocked for lock and park.
	Value int64  `json:"value" sort:"value"`
	Unit  string `json:"unit"`
}
