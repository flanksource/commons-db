package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing"
)

// Stack is a process's stacks as one dump took them: the text it dumped, or
// why it could not.
type Stack struct {
	Text        string    `json:"text,omitempty"`
	Error       string    `json:"error,omitempty"`
	CollectedAt time.Time `json:"collectedAt"`
}

// CheckpointRequest is what a process_tree checkpoint dumps: the stacks of
// the process Key or PID names, the root when neither does, either with Mode
// or as Stack, a dump its host took elsewhere, such as the goroutines a Go
// test printed as it was killed. No Mode and no Stack dumps with the first
// of the process's CaptureModes. Reason, such as "periodic", "timeout" or
// "manual", is recorded with the stacks.
type CheckpointRequest struct {
	Key    string      `json:"key,omitempty"`
	PID    int         `json:"pid,omitempty"`
	Mode   CaptureMode `json:"mode,omitempty"`
	Reason string      `json:"reason,omitempty"`
	Stack  *Stack      `json:"stack,omitempty"`
}

func (r CheckpointRequest) Validate() error {
	switch {
	case r.Key != "" && r.PID != 0:
		return fmt.Errorf("name a process by key %q or pid %d, not both", r.Key, r.PID)
	case r.PID < 0:
		return fmt.Errorf("pid %d is not a process id", r.PID)
	case r.Mode != "" && r.Mode != CaptureInProcess && r.Mode != CaptureNative:
		return fmt.Errorf("unknown capture mode %q (%s, %s)", r.Mode, CaptureInProcess, CaptureNative)
	case r.Stack == nil:
		return nil
	case r.Mode != "":
		return fmt.Errorf("a stack dumped elsewhere takes no capture mode, given %q", r.Mode)
	case r.Stack.CollectedAt.IsZero():
		return errors.New("a stack dumped elsewhere needs the collectedAt it was dumped at")
	case r.Stack.Text == "" && r.Stack.Error == "":
		return errors.New("a stack dumped elsewhere has neither text nor error")
	}
	return nil
}

// Checkpoint checkpoints managed, a process_tree capture of runtime, with
// request; it is committed when Checkpoint returns.
func Checkpoint(ctx context.Context, runtime *tracing.Runtime, managed *query.ManagedSession, request CheckpointRequest) error {
	params, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("checkpoint params: %w", err)
	}
	return runtime.Checkpoint(tracing.WithCheckpointParams(ctx, params), managed)
}

// Checkpoint observes the tree, then dumps the stacks its params ask for onto
// the process they name, live or exited. A dump that fails records why on
// the process, rather than failing the checkpoint; params that cannot be
// dumped, as a mode the process lacks, are tracing.ErrCheckpointParams.
func (t tree) Checkpoint(ctx dbcontext.Context, _ Params, records tracing.Emitter[Node]) error {
	var request CheckpointRequest
	if _, err := tracing.DecodeCheckpointParams(ctx, &request); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
	}
	run := ctx.Value(treeRunKey{}).(*treeRun)
	if err := t.observe(ctx, run, records); err != nil {
		return err
	}
	node, err := run.target(request)
	if err != nil {
		return err
	}
	stack, err := t.stack(ctx, node, request)
	if err != nil {
		return fmt.Errorf("%w: %w", tracing.ErrCheckpointParams, err)
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	node.Stack, node.StackError, node.StackReason = stack.Text, stack.Error, request.Reason
	node.StackCollectedAt = &stack.CollectedAt
	return records.Emit(ctx, *node)
}

// stack is the dump request asks for of node, whose pid and modes never
// change, so it is read without the run's lock while a dump takes its time.
func (t tree) stack(ctx context.Context, node *Node, request CheckpointRequest) (Stack, error) {
	if request.Stack != nil {
		return *request.Stack, nil
	}
	mode := request.Mode
	if mode == "" && len(node.CaptureModes) == 0 {
		err := errors.Join(t.available(CaptureInProcess, node.PID), t.available(CaptureNative, node.PID))
		return Stack{Error: err.Error(), CollectedAt: time.Now().UTC()}, nil
	}
	if mode == "" {
		mode = CaptureMode(node.CaptureModes[0])
	}
	if err := t.available(mode, node.PID); err != nil {
		return Stack{}, err
	}
	stack := Stack{CollectedAt: time.Now().UTC()}
	text, err := t.dump(ctx, mode, node.PID)
	if err != nil {
		stack.Error = err.Error()
	} else {
		stack.Text = text
	}
	return stack, nil
}

// target is the node request names: by key, by pid, the live process
// before an exited one and the latest to exit of those, or the root.
func (run *treeRun) target(request CheckpointRequest) (*Node, error) {
	run.mu.Lock()
	defer run.mu.Unlock()
	if request.Key != "" {
		if node := run.byKey(request.Key); node != nil {
			return node, nil
		}
		return nil, fmt.Errorf("%w: no process %q in the tree of %d", tracing.ErrCheckpointParams, request.Key, run.root)
	}
	var found *Node
	for _, node := range run.nodes() {
		matches := node.IsRoot
		if request.PID != 0 {
			matches = node.PID == request.PID
		}
		if matches && (found == nil || laterThan(node, found)) {
			found = node
		}
	}
	switch {
	case found != nil:
		return found, nil
	case request.PID != 0:
		return nil, fmt.Errorf("%w: no process with pid %d in the tree of %d", tracing.ErrCheckpointParams, request.PID, run.root)
	}
	return nil, fmt.Errorf("process %d: %w", run.root, errRootGone)
}

func (run *treeRun) byKey(key string) *Node {
	if node, ok := run.live[key]; ok {
		return node
	}
	return run.exited[key]
}

func (run *treeRun) nodes() []*Node {
	nodes := make([]*Node, 0, len(run.live)+len(run.exited))
	for _, node := range run.live {
		nodes = append(nodes, node)
	}
	for _, node := range run.exited {
		nodes = append(nodes, node)
	}
	return nodes
}

// laterThan reports whether a is a later process than b: live where b
// exited, or exited after it.
func laterThan(a, b *Node) bool {
	switch {
	case a.ExitedAt == nil:
		return b.ExitedAt != nil
	case b.ExitedAt == nil:
		return false
	}
	return a.ExitedAt.After(*b.ExitedAt)
}
