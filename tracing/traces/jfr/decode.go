package jfr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sort"
	"strings"
	"time"

	"github.com/grafana/jfr-parser/parser"
	"github.com/grafana/jfr-parser/parser/types"
	"github.com/grafana/jfr-parser/parser/types/def"
)

// magic opens every JFR chunk.
var magic = []byte("FLR\x00")

// truncatedFrame heads a stack whose root side was cut.
const truncatedFrame = "[truncated]"

// noStack is the stack of an event recorded without one, such as an
// allocation on a JIT compiler thread: JFR's null stack trace reference.
const noStack = "[no stack]"

// executionSample is the event of a jdk.ExecutionSample until the recording's
// settings say whether it sampled threads on CPU or by wall clock.
const executionSample = "execution"

// DecodeOptions say which of a recording's events Decode keeps and how it
// aggregates them.
type DecodeOptions struct {
	// After is the watermark: only events that end after it are decoded, so
	// a dump of the whole recording so far yields only what it added.
	After time.Time
	// Threads splits each stack's samples by the thread they were taken on.
	Threads bool
	// MaxStackDepth keeps only the leaf-most frames of deeper stacks; zero
	// keeps every frame.
	MaxStackDepth int
}

// Profile is what Decode read from a recording: a Sample per event, thread
// (when split by thread) and stack, the samples of each event, and the window
// of the events it decoded.
type Profile struct {
	Samples []Sample
	Counts  map[string]int64
	// From and Latest are the earliest and latest end of the events decoded;
	// both are zero when no event ended after DecodeOptions.After.
	From, Latest time.Time
}

// Decode reads the profiling events of a JFR recording, as the JDK's Flight
// Recorder or async-profiler writes it, one or more chunks long: execution and
// wall-clock samples, allocations, monitor waits and parks. It refuses an
// empty or malformed recording, and a stack, frame or thread its chunk's
// constant pool does not hold.
func Decode(data []byte, options DecodeOptions) (profile Profile, err error) {
	if len(data) == 0 {
		return Profile{}, errors.New("decode JFR: the recording is empty")
	}
	if !bytes.HasPrefix(data, magic) {
		return Profile{}, fmt.Errorf("decode JFR: not a JFR recording: it starts %q, not the chunk magic %q", data[:min(len(data), len(magic))], magic)
	}
	d := &decoder{
		parser:     parser.NewParser(data, parser.Options{SymbolProcessor: parser.ProcessSymbols}),
		options:    options,
		aggregates: map[key]*aggregate{},
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			profile, err = Profile{}, fmt.Errorf("decode JFR (%d bytes) after %d events: malformed recording: %v", len(data), d.events, recovered)
		}
	}()
	if err := d.run(); err != nil {
		return Profile{}, fmt.Errorf("decode JFR (%d bytes) after %d events: %w", len(data), d.events, err)
	}
	return d.profile(), nil
}

type key struct {
	event, thread, stack string
}

type aggregate struct {
	leaf           string
	samples, value int64
}

type stack struct {
	collapsed, leaf string
}

type decoder struct {
	parser  *parser.Parser
	options DecodeOptions

	// chunk is the header of the chunk being read; the caches resolve refs
	// through its constant pool, so a new chunk empties them.
	chunk   parser.ChunkHeader
	stacks  map[types.StackTraceRef]stack
	frames  map[types.MethodRef]string
	threads map[types.ThreadRef]string

	// wallClock is set by async-profiler's event=wall setting: its execution
	// samples are then of threads in any state.
	wallClock    bool
	aggregates   map[key]*aggregate
	events       int
	from, latest time.Time
}

func (d *decoder) run() error {
	p := d.parser
	for {
		typ, err := p.ParseEvent()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := d.enterChunk(p.ChunkHeader()); err != nil {
			return err
		}
		e, ok, err := d.occurrence(typ)
		if err != nil {
			return err
		}
		if ok {
			if err := d.add(e); err != nil {
				return fmt.Errorf("%s event: %w", eventName(e.event), err)
			}
		}
	}
}

// occurrence is one profiling event, its times in ticks of its chunk.
type occurrence struct {
	event           string
	start, duration uint64
	thread          types.ThreadRef
	stack           types.StackTraceRef
	samples, value  int64
}

// occurrence is the profiling event the parser just read as typ, if it is
// one; an async-profiler setting it reads instead is noted.
func (d *decoder) occurrence(typ def.TypeID) (occurrence, bool, error) {
	p := d.parser
	switch typ {
	case p.TypeMap.T_EXECUTION_SAMPLE:
		e := p.ExecutionSample
		return occurrence{event: executionSample, start: e.StartTime, thread: e.SampledThread, stack: e.StackTrace, samples: 1, value: 1}, true, nil
	case p.TypeMap.T_WALL_CLOCK_SAMPLE:
		e := p.WallClockSample
		if e.Samples == 0 {
			return occurrence{}, false, errors.New("wall-clock sample counts no samples: its recorder wrote no samples field")
		}
		return occurrence{event: EventWall, start: e.StartTime, thread: e.SampledThread, stack: e.StackTrace, samples: int64(e.Samples), value: int64(e.Samples)}, true, nil
	case p.TypeMap.T_ALLOC_IN_NEW_TLAB:
		e := p.ObjectAllocationInNewTLAB
		return occurrence{event: EventAlloc, start: e.StartTime, thread: e.EventThread, stack: e.StackTrace, samples: 1, value: int64(e.TlabSize)}, true, nil
	case p.TypeMap.T_ALLOC_OUTSIDE_TLAB:
		e := p.ObjectAllocationOutsideTLAB
		return occurrence{event: EventAlloc, start: e.StartTime, thread: e.EventThread, stack: e.StackTrace, samples: 1, value: int64(e.AllocationSize)}, true, nil
	case p.TypeMap.T_ALLOC_SAMPLE:
		e := p.ObjectAllocationSample
		return occurrence{event: EventAlloc, start: e.StartTime, thread: e.EventThread, stack: e.StackTrace, samples: 1, value: int64(e.Weight)}, true, nil
	case p.TypeMap.T_MONITOR_ENTER:
		e := p.JavaMonitorEnter
		return occurrence{event: EventLock, start: e.StartTime, duration: e.Duration, thread: e.EventThread, stack: e.StackTrace, samples: 1, value: d.nanos(e.Duration)}, true, nil
	case p.TypeMap.T_THREAD_PARK:
		e := p.ThreadPark
		return occurrence{event: EventPark, start: e.StartTime, duration: e.Duration, thread: e.EventThread, stack: e.StackTrace, samples: 1, value: d.nanos(e.Duration)}, true, nil
	case p.TypeMap.T_ACTIVE_SETTING:
		if p.ActiveSetting.Name == "event" && p.ActiveSetting.Value == EventWall {
			d.wallClock = true
		}
	}
	return occurrence{}, false, nil
}

// enterChunk starts resolving refs through header's chunk when it is not the
// one being read.
func (d *decoder) enterChunk(header parser.ChunkHeader) error {
	if header == d.chunk {
		return nil
	}
	if header.TicksPerSecond == 0 {
		return fmt.Errorf("chunk starting at %d ns has no tick frequency", header.StartNanos)
	}
	d.chunk = header
	d.stacks = map[types.StackTraceRef]stack{}
	d.frames = map[types.MethodRef]string{}
	d.threads = map[types.ThreadRef]string{}
	return nil
}

// add counts e on its thread's stack, unless it ended at or before the
// watermark.
func (d *decoder) add(e occurrence) error {
	end := d.instant(e.start + e.duration)
	if !end.After(d.options.After) {
		return nil
	}
	resolved, err := d.stack(e.stack)
	if err != nil {
		return err
	}
	k := key{event: e.event, stack: resolved.collapsed}
	if d.options.Threads {
		if k.thread, err = d.thread(e.thread); err != nil {
			return err
		}
	}
	counted, ok := d.aggregates[k]
	if !ok {
		counted = &aggregate{leaf: resolved.leaf}
		d.aggregates[k] = counted
	}
	counted.samples += e.samples
	counted.value += e.value
	d.events++
	if d.from.IsZero() || end.Before(d.from) {
		d.from = end
	}
	if end.After(d.latest) {
		d.latest = end
	}
	return nil
}

func eventName(event string) string {
	if event == executionSample {
		return "execution sample"
	}
	return event
}

// instant is the wall time of ticks in the chunk being read.
func (d *decoder) instant(ticks uint64) time.Time {
	start := time.Unix(0, int64(d.chunk.StartNanos))
	if ticks >= d.chunk.StartTicks {
		return start.Add(time.Duration(d.nanos(ticks - d.chunk.StartTicks))).UTC()
	}
	return start.Add(-time.Duration(d.nanos(d.chunk.StartTicks - ticks))).UTC()
}

// nanos converts a span of ticks of the chunk being read to nanoseconds,
// without overflowing on a long span at a high tick frequency.
func (d *decoder) nanos(ticks uint64) int64 {
	perSecond := d.chunk.TicksPerSecond
	hi, lo := bits.Mul64(ticks%perSecond, uint64(time.Second))
	fraction, _ := bits.Div64(hi, lo, perSecond)
	return int64(ticks/perSecond*uint64(time.Second) + fraction)
}

// stack collapses ref root first, keeping its leaf-most MaxStackDepth frames.
func (d *decoder) stack(ref types.StackTraceRef) (stack, error) {
	if ref == 0 {
		return stack{collapsed: noStack, leaf: noStack}, nil
	}
	if cached, ok := d.stacks[ref]; ok {
		return cached, nil
	}
	trace := d.parser.GetStacktrace(ref)
	if trace == nil {
		return stack{}, fmt.Errorf("stack trace %d is not in its chunk's constant pool", ref)
	}
	frames, truncated := trace.Frames, trace.Truncated
	if len(frames) == 0 {
		return stack{}, fmt.Errorf("stack trace %d has no frames", ref)
	}
	if limit := d.options.MaxStackDepth; limit > 0 && len(frames) > limit {
		frames, truncated = frames[:limit], true
	}
	names := make([]string, 0, len(frames)+1)
	if truncated {
		names = append(names, truncatedFrame)
	}
	for i := len(frames) - 1; i >= 0; i-- {
		name, err := d.frame(frames[i].Method)
		if err != nil {
			return stack{}, fmt.Errorf("stack trace %d: %w", ref, err)
		}
		names = append(names, name)
	}
	resolved := stack{collapsed: strings.Join(names, ";"), leaf: names[len(names)-1]}
	d.stacks[ref] = resolved
	return resolved, nil
}

// frame names method by its class and its own name, the class's binary name
// dotted: java.lang.Thread.run. A native library's frames keep its path.
func (d *decoder) frame(ref types.MethodRef) (string, error) {
	if cached, ok := d.frames[ref]; ok {
		return cached, nil
	}
	method := d.parser.GetMethod(ref)
	if method == nil {
		return "", fmt.Errorf("method %d is not in its chunk's constant pool", ref)
	}
	class := d.parser.GetClass(method.Type)
	if class == nil {
		return "", fmt.Errorf("class %d of method %d is not in its chunk's constant pool", method.Type, ref)
	}
	className, err := d.symbol(class.Name)
	if err != nil {
		return "", fmt.Errorf("class %d of method %d: %w", method.Type, ref, err)
	}
	methodName, err := d.symbol(method.Name)
	if err != nil {
		return "", fmt.Errorf("method %d: %w", ref, err)
	}
	name := methodName
	switch {
	case className == "":
	case strings.HasPrefix(className, "/"), strings.HasPrefix(className, "."):
		name = className + "." + methodName
	default:
		name = strings.ReplaceAll(className, "/", ".") + "." + methodName
	}
	d.frames[ref] = name
	return name, nil
}

func (d *decoder) symbol(ref types.SymbolRef) (string, error) {
	symbol := d.parser.GetSymbol(ref)
	if symbol == nil {
		return "", fmt.Errorf("symbol %d is not in its chunk's constant pool", ref)
	}
	return symbol.String, nil
}

// thread names ref by its Java name, or its OS name for a thread the JVM did
// not start.
func (d *decoder) thread(ref types.ThreadRef) (string, error) {
	if cached, ok := d.threads[ref]; ok {
		return cached, nil
	}
	index, ok := d.parser.Threads.IDMap[ref]
	if !ok {
		return "", fmt.Errorf("thread %d is not in its chunk's constant pool", ref)
	}
	thread := d.parser.Threads.Thread[index]
	name := thread.JavaName
	if name == "" {
		name = thread.OsName
	}
	if name == "" {
		return "", fmt.Errorf("thread %d has neither a Java nor an OS name", ref)
	}
	d.threads[ref] = name
	return name, nil
}

// profile is the decoded events as samples, an execution sample being of the
// event the recording's settings say it sampled.
func (d *decoder) profile() Profile {
	executionEvent := EventCPU
	if d.wallClock {
		executionEvent = EventWall
	}
	merged := map[key]*aggregate{}
	for k, counted := range d.aggregates {
		if k.event == executionSample {
			k.event = executionEvent
		}
		into, ok := merged[k]
		if !ok {
			merged[k] = &aggregate{leaf: counted.leaf, samples: counted.samples, value: counted.value}
			continue
		}
		into.samples += counted.samples
		into.value += counted.value
	}
	profile := Profile{Counts: map[string]int64{}, From: d.from, Latest: d.latest}
	for k, counted := range merged {
		profile.Samples = append(profile.Samples, Sample{
			Timestamp: d.latest, From: d.from, Event: k.event, Thread: k.thread,
			Frame: counted.leaf, Stack: k.stack, Samples: counted.samples, Value: counted.value, Unit: unitOf(k.event),
		})
		profile.Counts[k.event] += counted.samples
	}
	sort.Slice(profile.Samples, func(i, j int) bool {
		a, b := profile.Samples[i], profile.Samples[j]
		if a.Event != b.Event {
			return a.Event < b.Event
		}
		if a.Thread != b.Thread {
			return a.Thread < b.Thread
		}
		return a.Stack < b.Stack
	})
	return profile
}

func unitOf(event string) string {
	switch event {
	case EventAlloc:
		return UnitBytes
	case EventLock, EventPark:
		return UnitNanos
	default:
		return UnitSamples
	}
}
