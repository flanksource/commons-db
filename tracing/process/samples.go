package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// Sample is one process's usage at one tick: Key is its node's key in the
// process_tree stream of the same root.
type Sample struct {
	SampledAt time.Time `json:"sampledAt" pretty:"label=Sampled" sort:"sampledAt"`
	Tick      int64     `json:"tick" sort:"tick"`
	Key       string    `json:"key" pretty:"hide" filter:"exact"`
	PID       int       `json:"pid" pretty:"label=PID" filter:"terms"`
	// CPUPercent is the CPU% over the process's latest window of at least a
	// second, per core; nil until that window has passed.
	CPUPercent    *float64 `json:"cpuPercent,omitempty" pretty:"label=CPU %" sort:"cpuPercent"`
	MemoryPercent float64  `json:"memoryPercent" pretty:"label=Memory %" sort:"memoryPercent"`
	RSSBytes      uint64   `json:"rssBytes" pretty:"label=RSS,type=bytes" sort:"rssBytes"`
	// DiskReadBytes and DiskWriteBytes are what the process read from and
	// wrote to disk since it started; nil when it exited before they were read.
	DiskReadBytes  *uint64 `json:"diskReadBytes,omitempty" pretty:"label=Disk read,type=bytes" sort:"diskReadBytes"`
	DiskWriteBytes *uint64 `json:"diskWriteBytes,omitempty" pretty:"label=Disk written,type=bytes" sort:"diskWriteBytes"`
	// DiskIOError is why the disk counters could not be read, such as the
	// caller lacking permission over another user's or a setuid process.
	DiskIOError string `json:"diskIOError,omitempty" pretty:"label=Disk I/O error" filter:"none"`
	// The counts of each armed facet (SampleParams.Facets), every tick.
	OpenFiles   *int `json:"openFiles,omitempty" pretty:"label=Open files" sort:"openFiles"`
	Sockets     *int `json:"sockets,omitempty" pretty:"label=Sockets" sort:"sockets"`
	Ports       *int `json:"ports,omitempty" pretty:"label=Ports" sort:"ports"`
	Connections *int `json:"connections,omitempty" pretty:"label=Connections" sort:"connections"`
	// The lists of each armed facet, at the first tick and at each tick it
	// differs from the process's previous stored list; a list that emptied is
	// told by its count of 0.
	OpenFileList   []clickyprocess.Descriptor `json:"openFileList,omitempty" pretty:"hide" filter:"none"`
	SocketList     []clickyprocess.Descriptor `json:"socketList,omitempty" pretty:"hide" filter:"none"`
	PortList       []clickyprocess.Descriptor `json:"portList,omitempty" pretty:"hide" filter:"none"`
	ConnectionList []clickyprocess.Descriptor `json:"connectionList,omitempty" pretty:"hide" filter:"none"`
}

// SampleCheckpoint is what a process_samples checkpoint reads: a tick at
// once, with every armed facet's full list for the process Key names.
type SampleCheckpoint struct {
	Key string `json:"key"`
}

type samples struct {
	sampler *Sampler
}

func (samples) Params() SampleParams { return SampleParams{} }

func (samples) Schema() recordresults.ResultType[Sample] {
	return recordresults.ResultType[Sample]{
		Title: "Process samples", TimeColumn: "sampledAt", Follow: true,
		Indexes: []recordstore.IndexDef{{Columns: []string{"key", "sampledAt"}}},
	}
}

type samplesRunKey struct{}

// samplesRun is a capture's ticks: the latest one, and each process's facet
// lists as last stored, by key and facet.
type samplesRun struct {
	mu     sync.Mutex
	params SampleParams
	tick   int64
	stored map[string]map[string][]clickyprocess.Descriptor
}

// Prepare refuses a root that is not running, before the session starts.
func (s samples) Prepare(ctx dbcontext.Context, params SampleParams, _ tracing.Emitter[Sample]) (dbcontext.Context, func() error, error) {
	processes, _, err := s.sampler.subtree(ctx, params.root())
	if err != nil {
		return ctx, nil, err
	}
	if len(processes) == 0 {
		return ctx, nil, fmt.Errorf("process %d is not running", params.root())
	}
	run := &samplesRun{params: params, stored: map[string]map[string][]clickyprocess.Descriptor{}}
	return ctx.WithValue(samplesRunKey{}, run), nil, nil
}

// Handle samples the tree every interval until the capture stops or its root
// exits.
func (s samples) Handle(ctx dbcontext.Context, params SampleParams, records tracing.Emitter[Sample], _ tracing.Records[Sample]) error {
	interval, err := params.interval()
	if err != nil {
		return err
	}
	run := ctx.Value(samplesRunKey{}).(*samplesRun)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.sample(ctx, run, "", records); err != nil {
			// A tick the stop cancelled (its ps or lsof killed) is the stop, not a failure.
			if errors.Is(err, errRootGone) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Checkpoint takes a tick at once with the full lists of the process its
// params name (SampleCheckpoint); without params it reads nothing.
func (s samples) Checkpoint(ctx dbcontext.Context, params SampleParams, records tracing.Emitter[Sample]) error {
	var request SampleCheckpoint
	given, err := tracing.DecodeCheckpointParams(ctx, &request)
	switch {
	case err != nil || !given:
		return err
	case request.Key == "":
		return fmt.Errorf("%w: name the process whose facet lists to read by key", tracing.ErrCheckpointParams)
	case len(params.Facets) == 0:
		return fmt.Errorf("%w: the capture arms no facets to list for %q", tracing.ErrCheckpointParams, request.Key)
	}
	return s.sample(ctx, ctx.Value(samplesRunKey{}).(*samplesRun), request.Key, records)
}

// sample emits one tick of every live process of the tree, with the full
// facet lists of the process keyed force.
func (s samples) sample(ctx context.Context, run *samplesRun, force string, records tracing.Emitter[Sample]) error {
	processes, listedAt, err := s.sampler.subtree(ctx, run.params.root())
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		return errRootGone
	}
	if force != "" && !hasKey(processes, force) {
		return fmt.Errorf("%w: no live process %q in the tree of %d", tracing.ErrCheckpointParams, force, run.params.root())
	}
	io, descriptors, err := run.read(ctx, processes)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.tick++
	run.forget(processes)
	for _, p := range processes {
		sample := Sample{
			SampledAt: listedAt, Tick: run.tick, Key: Key(p.Process), PID: p.PID,
			CPUPercent: p.cpuPercent, MemoryPercent: p.MemoryPercent, RSSBytes: p.RSSBytes,
		}
		reading := io[p.PID]
		if counters := reading.IO; counters != nil {
			sample.DiskReadBytes, sample.DiskWriteBytes = &counters.DiskReadBytes, &counters.DiskWriteBytes
		}
		sample.DiskIOError = reading.IOError
		if held, ok := descriptors[p.PID]; ok {
			run.facets(&sample, held, sample.Key == force)
		}
		if err := records.Emit(ctx, sample); err != nil {
			return err
		}
	}
	return nil
}

func hasKey(processes []observed, key string) bool {
	for _, p := range processes {
		if Key(p.Process) == key {
			return true
		}
	}
	return false
}

// NewSamplesKind is the process_samples kind over sampler, whose sessions
// can follow the samples as they are taken.
func NewSamplesKind(sampler *Sampler) *tracing.Handler[SampleParams, Sample] {
	return tracing.NewHandler[SampleParams, Sample](samples{sampler: sampler}, tracing.Capabilities{Live: true})
}
