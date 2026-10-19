package process

import (
	"context"
	"errors"
	"fmt"
	"iter"
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

// maxValueBytes cuts a stored command or environment value longer than this.
const maxValueBytes = 64 << 10

// Node is one process of the tree as last seen: its stream holds each process
// once, replaced as it changes, and keeps it after it exits.
type Node struct {
	tracing.Truncation
	Key        string     `json:"key" pretty:"hide" filter:"exact"`
	ParentKey  string     `json:"parentKey,omitempty" pretty:"hide" filter:"exact"`
	PID        int        `json:"pid" pretty:"label=PID" filter:"terms"`
	PPID       int        `json:"ppid" pretty:"label=PPID"`
	Name       string     `json:"name" filter:"terms"`
	Command    string     `json:"command" filter:"text"`
	CWD        string     `json:"cwd,omitempty" pretty:"label=Cwd"`
	Status     string     `json:"status" filter:"terms"`
	IsRoot     bool       `json:"isRoot,omitempty" pretty:"hide"`
	StartedAt  time.Time  `json:"startedAt" pretty:"label=Started" sort:"startedAt"`
	ExitedAt   *time.Time `json:"exitedAt,omitempty" pretty:"label=Exited" sort:"exitedAt"`
	LastSeenAt time.Time  `json:"lastSeenAt" pretty:"hide"`
	// CPUPercent is the CPU% over the latest window of at least a second, per
	// core; nil until that window has passed. PeakCPUPercent is the highest
	// known one.
	CPUPercent     *float64 `json:"cpuPercent,omitempty" pretty:"label=CPU %" sort:"cpuPercent"`
	RSSBytes       uint64   `json:"rssBytes" pretty:"label=RSS,type=bytes" sort:"rssBytes"`
	PeakCPUPercent *float64 `json:"peakCpuPercent,omitempty" pretty:"label=Peak CPU %" sort:"peakCpuPercent"`
	PeakRSSBytes   uint64   `json:"peakRssBytes" pretty:"label=Peak RSS,type=bytes" sort:"peakRssBytes"`
	// StackTransports are the stack dump transports (the stacks kind's) its
	// stacks can be dumped with.
	StackTransports []StackTransport `json:"stackTransports,omitempty" pretty:"hide"`
	// Environment is the process's environment as its node was created, its
	// secrets masked, when the capture reads it (TreeParams.Environment);
	// EnvironmentError is why it could not be read.
	Environment      map[string]string `json:"environment,omitempty" pretty:"hide" filter:"none"`
	EnvironmentError string            `json:"environmentError,omitempty" pretty:"hide"`
}

// StackTransport is a transport a process's stacks can be dumped with, and
// whether dumping with it kills the process.
type StackTransport struct {
	Transport   string `json:"transport"`
	Destructive bool   `json:"destructive"`
}

// TransportLister lists the stack transports that can dump pid, as the
// stacks kind's Dumpers do.
type TransportLister interface {
	Transports(pid int) []StackTransport
}

// TreeOptions are the host's choices for the process_tree kind.
type TreeOptions struct {
	// Dumpers list the stack transports of each node; nil lists none.
	Dumpers TransportLister
}

type tree struct {
	sampler *Sampler
	dumpers TransportLister
}

func (tree) Params() TreeParams { return TreeParams{} }

func (tree) Schema() recordresults.ResultType[Node] {
	return recordresults.ResultType[Node]{
		Title: "Process tree", TimeColumn: "startedAt", KeyColumn: "key", OnConflict: recordstore.OnConflictReplace,
		Hierarchy:     &recordresults.HierarchyColumns{ID: "key", Parent: "parentKey"},
		SearchColumns: []string{"name", "command"},
	}
}

type treeRunKey struct{}

// treeRun is a capture's view of the tree: the processes alive at its latest
// tick, and those that exited, by key.
type treeRun struct {
	mu          sync.Mutex
	root        int
	interval    time.Duration
	environment *EnvironmentParams
	live        map[string]*Node
	exited      map[string]*Node
}

// Prepare refuses a root that is not running, before the session starts.
func (t tree) Prepare(ctx dbcontext.Context, params TreeParams, _ tracing.Emitter[Node]) (dbcontext.Context, func() error, error) {
	interval, err := params.interval()
	if err != nil {
		return ctx, nil, err
	}
	run := &treeRun{
		root: params.root(), interval: interval, environment: params.Environment,
		live: map[string]*Node{}, exited: map[string]*Node{},
	}
	processes, _, err := t.sampler.subtree(ctx, run.root)
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
func (t tree) Handle(ctx dbcontext.Context, _ TreeParams, records tracing.Emitter[Node], _ tracing.Records[Node]) error {
	run := ctx.Value(treeRunKey{}).(*treeRun)
	ticker := time.NewTicker(run.interval)
	defer ticker.Stop()
	for {
		err := t.observe(ctx, run, records)
		if ctx.Err() != nil {
			// Stopped, possibly mid-tick: the final observation records how the tree ended.
			return t.finalObserve(ctx, run, records)
		}
		if err != nil {
			if errors.Is(err, errRootGone) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return t.finalObserve(ctx, run, records)
		case <-ticker.C:
		}
	}
}

func (t tree) finalObserve(ctx dbcontext.Context, run *treeRun, records tracing.Emitter[Node]) error {
	err := t.observe(context.WithoutCancel(ctx), run, records)
	if errors.Is(err, errRootGone) {
		return nil
	}
	return err
}

// Checkpoint observes the tree, so every change since the previous tick is
// committed with the checkpoint.
func (t tree) Checkpoint(ctx dbcontext.Context, _ TreeParams, records tracing.Emitter[Node]) error {
	err := t.observe(ctx, ctx.Value(treeRunKey{}).(*treeRun), records)
	if errors.Is(err, errRootGone) {
		return nil
	}
	return err
}

// observe emits every process that started or changed since the previous
// tick, and every one that exited with when it was first missed. Once the
// root is missed, every process left is exited and errRootGone ends the run.
func (t tree) observe(ctx context.Context, run *treeRun, records tracing.Emitter[Node]) error {
	processes, listedAt, err := t.sampler.subtree(ctx, run.root)
	if err != nil {
		return err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	seen := make(map[string]bool, len(processes))
	keys := make(map[int]string, len(processes))
	for _, p := range processes {
		keys[p.PID] = Key(p.Process)
	}
	var started []observed
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
		run.exited[key] = node
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
// directories, stack transports and environments, which are read only once.
func (t tree) start(ctx context.Context, run *treeRun, started []observed, keys map[int]string, listedAt time.Time, records tracing.Emitter[Node]) error {
	if len(started) == 0 {
		return nil
	}
	pids := make([]int, 0, len(started))
	processes := make([]clickyprocess.Process, 0, len(started))
	for _, p := range started {
		pids, processes = append(pids, p.PID), append(processes, p.Process)
	}
	withCWD := clickyprocess.NewSnapshot(processes)
	withCWD.PopulateWorkingDirectories(ctx, pids)
	for _, p := range started {
		withDir, _ := withCWD.Get(p.PID)
		p.CWD = withDir.CWD
		node := newNode(p, keys[p.PPID], p.PID == run.root, listedAt)
		t.describe(ctx, run, node)
		run.live[node.Key] = node
		if err := records.Emit(ctx, *node); err != nil {
			return err
		}
	}
	return nil
}

// describe reads what a node keeps from its creation on: the transports its
// stacks can be dumped with and, when the capture reads it, its environment.
func (t tree) describe(ctx context.Context, run *treeRun, node *Node) {
	if t.dumpers != nil {
		node.StackTransports = t.dumpers.Transports(node.PID)
	}
	if run.environment == nil {
		return
	}
	environment, err := clickyprocess.Environment(ctx, node.PID, run.environment.options())
	if err != nil {
		node.EnvironmentError = err.Error()
		return
	}
	node.Environment = environment
}

func newNode(p observed, parentKey string, isRoot bool, listedAt time.Time) *Node {
	startedAt := listedAt
	if p.StartedAt != nil {
		startedAt = *p.StartedAt
	}
	node := &Node{
		Key: Key(p.Process), PID: p.PID, Name: name(p.Command), Command: p.Command, CWD: p.CWD,
		IsRoot: isRoot, StartedAt: startedAt,
	}
	node.update(p, parentKey, listedAt)
	return node
}

// update applies p's latest reading to n, reporting whether anything a
// reader sees changed. Only a known CPU% can raise its peak.
func (n *Node) update(p observed, parentKey string, listedAt time.Time) bool {
	changed := n.ParentKey != parentKey || n.PPID != p.PPID || n.Status != p.Status ||
		!equalPercent(n.CPUPercent, p.cpuPercent) || n.RSSBytes != p.RSSBytes
	n.ParentKey, n.PPID, n.Status = parentKey, p.PPID, p.Status
	n.CPUPercent, n.RSSBytes, n.LastSeenAt = p.cpuPercent, p.RSSBytes, listedAt
	if p.cpuPercent != nil && (n.PeakCPUPercent == nil || *p.cpuPercent > *n.PeakCPUPercent) {
		peak := *p.cpuPercent
		n.PeakCPUPercent = &peak
	}
	n.PeakRSSBytes = max(n.PeakRSSBytes, p.RSSBytes)
	return changed
}

func equalPercent(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Summarize is a window of the tree as the processes in it still live and
// those that exited, and the highest CPU and RSS any of them reached; the
// CPU peak is left out while no process has a known one.
func (tree) Summarize(rows iter.Seq[Node]) (map[string]any, error) {
	live, exited := 0, 0
	var peakCPU *float64
	var peakRSS uint64
	for node := range rows {
		if node.ExitedAt == nil {
			live++
		} else {
			exited++
		}
		if node.PeakCPUPercent != nil && (peakCPU == nil || *node.PeakCPUPercent > *peakCPU) {
			peakCPU = node.PeakCPUPercent
		}
		peakRSS = max(peakRSS, node.PeakRSSBytes)
	}
	summary := map[string]any{"live": live, "exited": exited, "peak_rss_bytes": peakRSS}
	if peakCPU != nil {
		summary["peak_cpu_percent"] = *peakCPU
	}
	return summary, nil
}

// NewTreeKind is the process_tree kind over sampler: each process stored
// once and replaced as it changes, its secrets masked and its values cut at
// 64KiB.
func NewTreeKind(sampler *Sampler, options TreeOptions) *tracing.Handler[TreeParams, Node] {
	return tracing.NewHandler[TreeParams, Node](tree{sampler: sampler, dumpers: options.Dumpers}, tracing.Capabilities{Live: true}).
		WithSecretMasking("key").WithTruncation(maxValueBytes)
}
