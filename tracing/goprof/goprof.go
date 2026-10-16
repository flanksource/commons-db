// Package goprof is a trace kind that reads the pprof profiles of the Go
// processes of a tree at each checkpoint its host marks: over a process's
// net/http/pprof server or gops agent, or from a -cpuprofile/-memprofile file
// a test binary wrote. It stores each profile as its samples aggregated by
// stack, one record per sample type and stack, and exports them as pprof.
package goprof

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/stacks"
)

// Kind is the name a host registers the kind as.
const Kind = "goprof"

// The sources a profile is read from.
const (
	SourcePProf = "pprof"
	SourceGops  = "gops"
	SourceFile  = "file"
)

// Profiles are the profiles a checkpoint reads.
var Profiles = []string{"cpu", "heap", "allocs", "mutex", "block", "goroutine"}

// gopsCPUSeconds is how long a gops agent profiles CPU for, which it does not
// let its client choose.
const gopsCPUSeconds = 30

// Params choose the tree whose processes a capture profiles.
type Params struct {
	// RootPID is the process whose tree is profiled; zero is the host's own.
	RootPID int `json:"rootPid,omitempty" clicky:"title=Root PID"`
}

func (p Params) Validate() error {
	if p.RootPID < 0 {
		return fmt.Errorf("rootPid %d is not a process id", p.RootPID)
	}
	return nil
}

func (p Params) root() int {
	if p.RootPID == 0 {
		return os.Getpid()
	}
	return p.RootPID
}

// Sample is the record the kind stores: the value one stack holds of one
// sample type of a profile, its frames innermost first.
type Sample struct {
	CheckpointID string         `json:"checkpointId" pretty:"hide" filter:"exact"`
	TakenAt      time.Time      `json:"takenAt" pretty:"label=Taken" sort:"takenAt"`
	Reason       string         `json:"reason,omitempty" filter:"terms"`
	Source       string         `json:"source" filter:"terms"`
	Key          string         `json:"key" pretty:"hide" filter:"exact"`
	PID          int            `json:"pid" pretty:"label=PID" filter:"terms"`
	Profile      string         `json:"profile" filter:"terms"`
	SampleType   string         `json:"sampleType" filter:"terms"`
	Unit         string         `json:"unit"`
	Value        int64          `json:"value" sort:"value"`
	Frames       []stacks.Frame `json:"frames" pretty:"hide"`
	StackHash    string         `json:"stackHash" pretty:"hide" filter:"exact"`
}

// CheckpointParams are what a checkpoint reads: Profile of the process Key
// or PID names, the root when neither does, from Source. A cpu profile over
// pprof runs for Seconds; a gops agent runs it for 30s. A file source reads
// Path, a profile a process that may have exited wrote, recorded under the
// key or pid given. Reason is recorded with the samples.
type CheckpointParams struct {
	Key     string `json:"key,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Source  string `json:"source"`
	Profile string `json:"profile"`
	Seconds int    `json:"seconds,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func (p CheckpointParams) Validate() error {
	var errs []error
	if p.Key != "" && p.PID != 0 {
		errs = append(errs, fmt.Errorf("name a process by key %q or pid %d, not both", p.Key, p.PID))
	}
	if p.PID < 0 || p.Seconds < 0 {
		errs = append(errs, fmt.Errorf("pid %d and seconds %d must not be negative", p.PID, p.Seconds))
	}
	if !slices.Contains(Profiles, p.Profile) {
		errs = append(errs, fmt.Errorf("profile %q: expected one of %v", p.Profile, Profiles))
	}
	if p.Seconds != 0 && (p.Profile != "cpu" || p.Source == SourceFile) {
		errs = append(errs, fmt.Errorf("seconds is how long a live cpu profile runs, not a %s %s profile's", p.Source, p.Profile))
	}
	if (p.Path != "") != (p.Source == SourceFile) {
		errs = append(errs, errors.New("a path is what a file source, and only it, reads"))
	}
	return errors.Join(append(errs, p.validateSource())...)
}

func (p CheckpointParams) validateSource() error {
	switch p.Source {
	case SourceFile:
		if p.Key == "" && p.PID == 0 {
			return errors.New("a profile file needs the key or pid of the process that wrote it")
		}
	case SourcePProf:
		if p.Profile == "cpu" && p.Seconds == 0 {
			return errors.New("a cpu profile over pprof needs the seconds it runs for")
		}
	case SourceGops:
		if p.Profile != "cpu" && p.Profile != "heap" {
			return fmt.Errorf("a gops agent profiles cpu and heap, not %s", p.Profile)
		}
		if p.Seconds != 0 && p.Seconds != gopsCPUSeconds {
			return fmt.Errorf("a gops agent profiles cpu for %ds, not %ds", gopsCPUSeconds, p.Seconds)
		}
	default:
		return fmt.Errorf("source %q: expected %q, %q or %q", p.Source, SourcePProf, SourceGops, SourceFile)
	}
	return nil
}

type profiler struct {
	address stacks.AddressFunc
}

func (profiler) Params() Params { return Params{} }

func (profiler) Schema() recordresults.ResultType[Sample] {
	return recordresults.ResultType[Sample]{
		Title: "Go profiles", TimeColumn: "takenAt", Follow: true,
		Indexes:       []recordstore.IndexDef{{Columns: []string{"key", "takenAt"}}},
		SearchColumns: []string{"frames"},
	}
}

// Prepare refuses a root that is not running, before the session starts.
func (profiler) Prepare(ctx dbcontext.Context, params Params, _ tracing.Emitter[Sample]) (dbcontext.Context, func() error, error) {
	if _, err := stacks.Resolve(ctx, params.root(), "", 0); err != nil {
		return ctx, nil, err
	}
	return ctx, nil, nil
}

// Handle waits for the capture to stop: the kind reads only at checkpoints.
func (profiler) Handle(ctx dbcontext.Context, _ Params, _ tracing.Emitter[Sample], _ tracing.Records[Sample]) error {
	<-ctx.Done()
	return nil
}

// NewKind is the goprof kind, reading pprof servers and gops agents at the
// addresses address gives; nil reads only profile files.
func NewKind(address stacks.AddressFunc) *tracing.Handler[Params, Sample] {
	return tracing.NewHandler[Params, Sample](profiler{address: address}, tracing.Capabilities{Live: true})
}
