package workspace

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
)

// Status is how a path changed between two states.
type Status string

const (
	StatusAdded       Status = "added"
	StatusModified    Status = "modified"
	StatusDeleted     Status = "deleted"
	StatusRenamed     Status = "renamed"
	StatusCopied      Status = "copied"
	StatusTypeChanged Status = "type-changed"
)

var statusCodes = map[byte]Status{
	'A': StatusAdded, 'M': StatusModified, 'D': StatusDeleted,
	'R': StatusRenamed, 'C': StatusCopied, 'T': StatusTypeChanged,
}

// Change is one changed path; From is the original path of a rename or copy.
type Change struct {
	Status Status `json:"status"`
	Path   string `json:"path"`
	From   string `json:"from,omitempty"`
}

// DiffOptions selects the two states to compare. Each is a revision — a
// snapshot commit, refs/snapshots/<name>, or any commit-ish — and empty means
// the workspace's current working state.
type DiffOptions struct {
	From, To string
	// Patch also renders a binary-safe unified diff.
	Patch bool
}

type DiffResult struct {
	Changes []Change `json:"changes"`
	Patch   string   `json:"patch,omitempty"`
}

// Diff compares two states of the workspace.
func (w *Workspace) Diff(ctx context.Context, opts DiffOptions) (*DiffResult, error) {
	from, err := w.resolveTree(ctx, opts.From)
	if err != nil {
		return nil, err
	}
	to, err := w.resolveTree(ctx, opts.To)
	if err != nil {
		return nil, err
	}
	changes, err := diffTrees(ctx, w.Dir, from, to)
	if err != nil {
		return nil, err
	}
	result := &DiffResult{Changes: changes}
	if opts.Patch {
		patch, err := gitOutput(ctx, w.Dir, "diff-tree", "-p", "--binary", "-M", from, to)
		if err != nil {
			return nil, err
		}
		result.Patch = string(patch)
	}
	return result, nil
}

func (w *Workspace) resolveTree(ctx context.Context, rev string) (string, error) {
	if rev == "" {
		return w.workingTree(ctx)
	}
	return gitString(ctx, w.Dir, "rev-parse", "--verify", "--end-of-options", rev+"^{tree}")
}

// Outgoing is the net change between two commits; an empty from means the
// empty tree, so every path in to is reported as added.
func (w *Workspace) Outgoing(ctx context.Context, from, to string) ([]Change, error) {
	if from == "" {
		var err error
		if from, err = emptyTree(ctx, w.Dir); err != nil {
			return nil, err
		}
	}
	return diffTrees(ctx, w.Dir, from, to)
}

func emptyTree(ctx context.Context, dir string) (string, error) {
	return gitCmd{Dir: dir, Stdin: []byte{}}.string(ctx, "hash-object", "-t", "tree", "--stdin")
}

func diffTrees(ctx context.Context, dir, from, to string) ([]Change, error) {
	out, err := gitOutput(ctx, dir, "diff-tree", "-r", "-z", "--name-status", "-M", from, to)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out, from+".."+to)
}

// parseNameStatus parses `--name-status -z` output; what names the compared
// states for errors.
func parseNameStatus(out []byte, what string) ([]Change, error) {
	records := splitNUL(out)
	var changes []Change
	for i := 0; i < len(records); i++ {
		code := records[i]
		status, ok := statusCodes[code[0]]
		if !ok {
			return nil, fmt.Errorf("workspace: diff %s: unknown status %q", what, code)
		}
		paths := 1
		if status == StatusRenamed || status == StatusCopied {
			paths = 2
		}
		if i+paths >= len(records) {
			return nil, fmt.Errorf("workspace: diff %s: truncated record after %q", what, code)
		}
		change := Change{Status: status, Path: records[i+paths]}
		if paths == 2 {
			change.From = records[i+1]
		}
		changes = append(changes, change)
		i += paths
	}
	return changes, nil
}
