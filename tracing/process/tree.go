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

// TreeKind and SamplesKind are the names a host registers the kinds as.
const (
	TreeKind    = "process_tree"
	SamplesKind = "process_samples"
)

// maxStackBytes cuts a stored stack longer than this.
const maxStackBytes = 1 << 20

// Node is one process of the tree as last seen: its stream holds each process
// once, replaced as it changes, and keeps it after it exits.
type Node struct {
	tracing.Truncation
	Key            string     `json:"key" pretty:"hide" filter:"exact"`
	ParentKey      string     `json:"parentKey,omitempty" pretty:"hide" filter:"exact"`
	PID            int        `json:"pid" pretty:"label=PID" filter:"terms"`
	PPID           int        `json:"ppid" pretty:"label=PPID"`
	Name           string     `json:"name" filter:"terms"`
	Command        string     `json:"command" filter:"text"`
	CWD            string     `json:"cwd,omitempty" pretty:"label=Cwd"`
	Status         string     `json:"status" filter:"terms"`
	IsRoot         bool       `json:"isRoot,omitempty" pretty:"hide"`
	StartedAt      time.Time  `json:"startedAt" pretty:"label=Started" sort:"startedAt"`
	ExitedAt       *time.Time `json:"exitedAt,omitempty" pretty:"label=Exited" sort:"exitedAt"`
	LastSeenAt     time.Time  `json:"lastSeenAt" pretty:"hide"`
	CPUPercent     float64    `json:"cpuPercent" pretty:"label=CPU %" sort:"cpuPercent"`
	RSSBytes       uint64     `json:"rssBytes" pretty:"label=RSS,type=bytes" sort:"rssBytes"`
	PeakCPUPercent float64    `json:"peakCpuPercent" pretty:"label=Peak CPU %" sort:"peakCpuPercent"`
	PeakRSSBytes   uint64     `json:"peakRssBytes" pretty:"label=Peak RSS,type=bytes" sort:"peakRssBytes"`
	// Stack is the root's goroutine dump at the latest checkpoint, when the
	// root is the host itself.
	Stack            string     `json:"stack,omitempty" pretty:"hide" filter:"none"`
	StackCollectedAt *time.Time `json:"stackCollectedAt,omitempty" pretty:"hide"`
	StackError       string     `json:"stackError,omitempty" pretty:"hide"`
}

// TreeOptions are the host's choices for the process_tree kind.
type TreeOptions struct {
	// Stack dumps the stacks of the process pid at a checkpoint. Nil dumps the
	// goroutines of the host itself, and refuses any other process.
	Stack func(pid int) (string, error)
}

type tree struct {
	sampler *Sampler
	stack   func(pid int) (string, error)
}

func (tree) Params() Params { return Params{} }

func (tree) Schema() recordresults.ResultType[Node] {
	return recordresults.ResultType[Node]{
		Title: "Process tree", TimeColumn: "startedAt", KeyColumn: "key", OnConflict: recordstore.OnConflictReplace,
		Hierarchy:  &recordresults.HierarchyColumns{ID: "key", Parent: "parentKey"},
		Compressed: []string{"stack"}, SearchColumns: []string{"name", "command"},
	}
}

type treeRunKey struct{}

// treeRun is a capture's view of the tree: the processes alive at its latest
// tick, by key.
type treeRun struct {
	mu       sync.Mutex
	root     int
	interval time.Duration
	live     map[string]*Node
}

// Prepare refuses a root that is not running, before the session starts.
func (t tree) Prepare(ctx dbcontext.Context, params Params, _ tracing.Emitter[Node]) (dbcontext.Context, func() error, error) {
	interval, err := params.interval()
	if err != nil {
		return ctx, nil, err
	}
	run := &treeRun{root: params.root(), interval: interval, live: map[string]*Node{}}
	processes, _, err := t.sampler.Subtree(ctx, run.root)
	if err != nil {
		return ctx, nil, err
	}
	if len(processes) == 0 {
		return ctx, nil, fmt.Errorf("process %d is not running", run.root)
	}
	return ctx.WithValue(treeRunKey{}, run), nil, nil
}

// Handle observes the tree every interval until the capture stops or its root
// exits, then once more under a context the stop does not cancel.
func (t tree) Handle(ctx dbcontext.Context, _ Params, records tracing.Emitter[Node], _ tracing.Records[Node]) error {
	run := ctx.Value(treeRunKey{}).(*treeRun)
	ticker := time.NewTicker(run.interval)
	defer ticker.Stop()
	for {
		if err := t.observe(ctx, run, records); err != nil {
			if errors.Is(err, errRootGone) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			err := t.observe(context.WithoutCancel(ctx), run, records)
			if errors.Is(err, errRootGone) {
				return nil
			}
			return err
		case <-ticker.C:
		}
	}
}

// observe emits every process that started or changed since the previous
// tick, and every one that exited with when it was first missed. Once the
// root is missed, every process left is exited and errRootGone ends the run.
func (t tree) observe(ctx context.Context, run *treeRun, records tracing.Emitter[Node]) error {
	processes, listedAt, err := t.sampler.Subtree(ctx, run.root)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	seen := make(map[string]bool, len(processes))
	keys := make(map[int]string, len(processes))
	for _, p := range processes {
		keys[p.PID] = Key(p)
	}
	var started []clickyprocess.Process
	for _, p := range processes {
		key := keys[p.PID]
		seen[key] = true
		node, ok := run.live[key]
		if !ok {
			started = append(started, p)
			continue
		}
		if node.update(p, keys[p.PPID], listedAt) {
			if err := records.Emit(ctx, *node); err != nil {
				return err
			}
		}
	}
	if err := t.start(ctx, run, started, keys, listedAt, records); err != nil {
		return err
	}
	for key, node := range run.live {
		if seen[key] {
			continue
		}
		exitedAt := listedAt
		node.ExitedAt, node.Status = &exitedAt, "exited"
		delete(run.live, key)
		if err := records.Emit(ctx, *node); err != nil {
			return err
		}
	}
	if len(processes) == 0 {
		return errRootGone
	}
	return nil
}

// start emits the processes seen for the first time, with their working
// directories, which are read only once.
func (t tree) start(ctx context.Context, run *treeRun, started []clickyprocess.Process, keys map[int]string, listedAt time.Time, records tracing.Emitter[Node]) error {
	if len(started) == 0 {
		return nil
	}
	pids := make([]int, 0, len(started))
	for _, p := range started {
		pids = append(pids, p.PID)
	}
	withCWD := clickyprocess.NewSnapshot(started)
	withCWD.PopulateWorkingDirectories(ctx, pids)
	for _, pid := range pids {
		p, _ := withCWD.Get(pid)
		node := newNode(p, keys[p.PPID], pid == run.root, listedAt)
		run.live[node.Key] = node
		if err := records.Emit(ctx, *node); err != nil {
			return err
		}
	}
	return nil
}

func newNode(p clickyprocess.Process, parentKey string, isRoot bool, listedAt time.Time) *Node {
	startedAt := listedAt
	if p.StartedAt != nil {
		startedAt = *p.StartedAt
	}
	node := &Node{
		Key: Key(p), PID: p.PID, Name: name(p.Command), Command: p.Command, CWD: p.CWD,
		IsRoot: isRoot, StartedAt: startedAt,
	}
	node.update(p, parentKey, listedAt)
	return node
}

// update applies p's latest reading to n, reporting whether anything a
// reader sees changed.
func (n *Node) update(p clickyprocess.Process, parentKey string, listedAt time.Time) bool {
	changed := n.ParentKey != parentKey || n.PPID != p.PPID || n.Status != p.Status ||
		n.CPUPercent != p.CPUPercent || n.RSSBytes != p.RSSBytes
	n.ParentKey, n.PPID, n.Status = parentKey, p.PPID, p.Status
	n.CPUPercent, n.RSSBytes, n.LastSeenAt = p.CPUPercent, p.RSSBytes, listedAt
	n.PeakCPUPercent = max(n.PeakCPUPercent, p.CPUPercent)
	n.PeakRSSBytes = max(n.PeakRSSBytes, p.RSSBytes)
	return changed
}

// Checkpoint observes the tree, then dumps the root's stacks onto its node. A
// root whose stacks cannot be dumped records why, rather than failing the
// checkpoint.
func (t tree) Checkpoint(ctx dbcontext.Context, _ Params, records tracing.Emitter[Node]) error {
	run := ctx.Value(treeRunKey{}).(*treeRun)
	if err := t.observe(ctx, run, records); err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	var root *Node
	for _, node := range run.live {
		if node.IsRoot {
			root = node
		}
	}
	if root == nil {
		return fmt.Errorf("process %d: %w", run.root, errRootGone)
	}
	collectedAt := time.Now().UTC()
	root.StackCollectedAt = &collectedAt
	root.Stack, root.StackError = "", ""
	if stack, err := t.stack(root.PID); err != nil {
		root.StackError = err.Error()
	} else {
		root.Stack = stack
	}
	return records.Emit(ctx, *root)
}

// NewTreeKind is the process_tree kind over sampler: each process stored
// once and replaced as it changes, its root's stacks cut at 1MiB.
func NewTreeKind(sampler *Sampler, options TreeOptions) *tracing.Handler[Params, Node] {
	stack := options.Stack
	if stack == nil {
		stack = hostStack
	}
	return tracing.NewHandler[Params, Node](tree{sampler: sampler, stack: stack}, tracing.Capabilities{Live: true}).
		WithTruncation(maxStackBytes)
}
