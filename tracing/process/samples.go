package process

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// Sample is one process's usage at one tick: Key is its node's key in the
// process_tree stream of the same root.
type Sample struct {
	SampledAt     time.Time `json:"sampledAt" pretty:"label=Sampled" sort:"sampledAt"`
	Tick          int64     `json:"tick" sort:"tick"`
	Key           string    `json:"key" pretty:"hide" filter:"exact"`
	PID           int       `json:"pid" pretty:"label=PID" filter:"terms"`
	CPUPercent    float64   `json:"cpuPercent" pretty:"label=CPU %" sort:"cpuPercent"`
	MemoryPercent float64   `json:"memoryPercent" pretty:"label=Memory %" sort:"memoryPercent"`
	RSSBytes      uint64    `json:"rssBytes" pretty:"label=RSS,type=bytes" sort:"rssBytes"`
}

type samples struct {
	sampler *Sampler
}

func (samples) Params() Params { return Params{} }

func (samples) Schema() recordresults.ResultType[Sample] {
	return recordresults.ResultType[Sample]{
		Title: "Process samples", TimeColumn: "sampledAt", Follow: true,
		Indexes: []recordstore.IndexDef{{Columns: []string{"key", "sampledAt"}}},
	}
}

// Prepare refuses a root that is not running, before the session starts.
func (s samples) Prepare(ctx dbcontext.Context, params Params, _ tracing.Emitter[Sample]) (dbcontext.Context, func() error, error) {
	processes, _, err := s.sampler.Subtree(ctx, params.root())
	if err != nil {
		return ctx, nil, err
	}
	if len(processes) == 0 {
		return ctx, nil, fmt.Errorf("process %d is not running", params.root())
	}
	return ctx, nil, nil
}

// Handle samples the tree every interval until the capture stops or its root
// exits.
func (s samples) Handle(ctx dbcontext.Context, params Params, records tracing.Emitter[Sample], _ tracing.Records[Sample]) error {
	interval, err := params.interval()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for tick := int64(1); ; tick++ {
		if err := s.sample(ctx, params.root(), tick, records); err != nil {
			if errors.Is(err, errRootGone) {
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

func (s samples) sample(ctx context.Context, root int, tick int64, records tracing.Emitter[Sample]) error {
	processes, listedAt, err := s.sampler.Subtree(ctx, root)
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		return errRootGone
	}
	for _, p := range processes {
		if err := records.Emit(ctx, Sample{
			SampledAt: listedAt, Tick: tick, Key: Key(p), PID: p.PID,
			CPUPercent: p.CPUPercent, MemoryPercent: p.MemoryPercent, RSSBytes: p.RSSBytes,
		}); err != nil {
			return err
		}
	}
	return nil
}

// NewSamplesKind is the process_samples kind over sampler, whose sessions
// can follow the samples as they are taken.
func NewSamplesKind(sampler *Sampler) *tracing.Handler[Params, Sample] {
	return tracing.NewHandler[Params, Sample](samples{sampler: sampler}, tracing.Capabilities{Live: true})
}
