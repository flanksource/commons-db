// Package stacks is a trace kind that dumps the stacks of the processes of a
// tree at each checkpoint its host marks, storing one record per goroutine of
// a Go dump, or one per dump of a native one. Dumps go over the transports a
// host registers in Dumpers.
package stacks

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// Kind is the name a host registers the kind as.
const Kind = "stacks"

// maxRawBytes cuts a stored dump longer than this.
const maxRawBytes = 1 << 20

// Params choose the tree whose processes a capture dumps.
type Params struct {
	// RootPID is the process whose tree is dumped; zero is the host's own.
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

// Frame is one call of a stack.
type Frame struct {
	Func string `json:"func"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Stack is the record the kind stores: one goroutine of a Go dump, or the
// whole of a native one, or why a dump failed.
type Stack struct {
	tracing.Truncation
	CheckpointID string    `json:"checkpointId" pretty:"hide" filter:"exact"`
	TakenAt      time.Time `json:"takenAt" pretty:"label=Taken" sort:"takenAt"`
	Reason       string    `json:"reason,omitempty" filter:"terms"`
	Transport    string    `json:"transport" filter:"terms"`
	Key          string    `json:"key" pretty:"hide" filter:"exact"`
	PID          int       `json:"pid" pretty:"label=PID" filter:"terms"`
	Format       string    `json:"format" filter:"terms"`
	GoroutineID  int       `json:"goroutineId,omitempty" pretty:"label=Goroutine"`
	State        string    `json:"state,omitempty" filter:"terms"`
	// WaitMinutes is how long the goroutine has waited in its state, as the
	// dump rounds it.
	WaitMinutes    int     `json:"waitMinutes,omitempty" pretty:"label=Waiting (min)" sort:"waitMinutes"`
	LockedToThread bool    `json:"lockedToThread,omitempty"`
	Frames         []Frame `json:"frames,omitempty" pretty:"hide"`
	CreatedBy      *Frame  `json:"createdBy,omitempty" pretty:"hide"`
	Raw            string  `json:"raw" pretty:"hide" filter:"none"`
	Error          string  `json:"error,omitempty"`
}

// CheckpointParams are what a checkpoint dumps: the stacks of the process
// Key or PID names, the root when neither does, over Transport, the first
// available non-destructive one when empty, or Dump, a dump taken elsewhere,
// such as the goroutines a Go test printed as SIGQUIT killed it, which takes
// no transport. Reason, such as "periodic", "timeout" or "manual", is
// recorded with the stacks.
type CheckpointParams struct {
	Key       string `json:"key,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Transport string `json:"transport,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Dump      *Dump  `json:"dump,omitempty"`
}

func (p CheckpointParams) Validate() error {
	switch {
	case p.Key != "" && p.PID != 0:
		return fmt.Errorf("name a process by key %q or pid %d, not both", p.Key, p.PID)
	case p.PID < 0:
		return fmt.Errorf("pid %d is not a process id", p.PID)
	case p.Dump == nil:
		return nil
	case p.Transport != "":
		return fmt.Errorf("a dump taken elsewhere takes no transport, given %q", p.Transport)
	case p.Key == "" && p.PID == 0:
		return errors.New("a dump taken elsewhere needs the key or pid of the process it dumped")
	}
	return p.Dump.Validate()
}

type stacks struct {
	dumpers *Dumpers
}

func (stacks) Params() Params { return Params{} }

func (stacks) Schema() recordresults.ResultType[Stack] {
	return recordresults.ResultType[Stack]{
		Title: "Stack dumps", TimeColumn: "takenAt", Follow: true,
		Indexes:    []recordstore.IndexDef{{Columns: []string{"key", "takenAt"}}},
		Compressed: []string{"raw"}, SearchColumns: []string{"state", "frames"},
	}
}

// Prepare refuses a root that is not running, before the session starts.
func (stacks) Prepare(ctx dbcontext.Context, params Params, _ tracing.Emitter[Stack]) (dbcontext.Context, func() error, error) {
	if _, err := Resolve(ctx, params.root(), "", 0); err != nil {
		return ctx, nil, err
	}
	return ctx, nil, nil
}

// Handle waits for the capture to stop: the kind reads only at checkpoints.
func (stacks) Handle(ctx dbcontext.Context, _ Params, _ tracing.Emitter[Stack], _ tracing.Records[Stack]) error {
	<-ctx.Done()
	return nil
}

// NewKind is the stacks kind dumping over dumpers, its dumps cut at 1MiB.
func NewKind(dumpers *Dumpers) *tracing.Handler[Params, Stack] {
	if dumpers == nil {
		panic("stacks: NewKind needs the host's Dumpers")
	}
	return tracing.NewHandler[Params, Stack](stacks{dumpers: dumpers}, tracing.Capabilities{Live: true}).
		WithTruncation(maxRawBytes)
}

func newCheckpointID() string { return uuid.NewString() }
