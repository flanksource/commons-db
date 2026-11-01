package workspace

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// PullOptions configures Workspace.Pull. With Remote empty the snapshot is
// read from this repository's own refs, which every worktree of it shares.
type PullOptions struct {
	Remote string
	Name   string
	// Force overwrites local changes the snapshot does not descend from.
	Force bool
}

// Pull makes the working state match a snapshot: files it holds are written,
// files it lacks are removed, and gitignored content is left alone. HEAD and
// the index are not moved, so the pulled state shows as uncommitted work.
//
// Without Force a pull must fast-forward: HEAD must be the snapshot's Base and
// the working state must be clean or equal to a snapshot this one descends
// from. Anything else fails, naming the local changes that would be lost.
func (w *Workspace) Pull(ctx context.Context, opts PullOptions) (*Snapshot, error) {
	ref, err := snapshotRef(ctx, w.Dir, opts.Name)
	if err != nil {
		return nil, err
	}
	rev := ref
	if opts.Remote != "" {
		if _, err := gitOutput(ctx, w.Dir, "fetch", "--quiet", "--no-tags", opts.Remote, ref); err != nil {
			return nil, err
		}
		rev = "FETCH_HEAD"
	}
	snap, err := readSnapshot(ctx, w.Dir, opts.Name, rev)
	if err != nil {
		return nil, err
	}
	current, err := w.workingTree(ctx)
	if err != nil {
		return nil, err
	}
	if current == snap.Tree {
		return snap, nil
	}
	if !opts.Force {
		if err := w.requireFastForward(ctx, snap, current); err != nil {
			return nil, err
		}
	}
	return snap, w.checkoutTree(ctx, current, snap.Tree)
}

func (w *Workspace) requireFastForward(ctx context.Context, snap *Snapshot, current string) error {
	head, err := gitString(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != snap.Base {
		return fmt.Errorf("workspace: snapshot %s is based on %s but %s is at %s; check out the base first or pass Force",
			snap.Name, snap.Base, w.Dir, head)
	}
	expected, err := gitString(ctx, w.Dir, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if current == expected {
		return nil
	}
	for previous := snap.Previous; previous != ""; {
		ancestor, err := readSnapshot(ctx, w.Dir, snap.Name, previous)
		if err != nil {
			return err
		}
		if ancestor.Tree == current {
			return nil
		}
		previous = ancestor.Previous
	}
	changes, err := diffTrees(ctx, w.Dir, expected, current)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return fmt.Errorf("workspace: pulling snapshot %s into %s is not a fast-forward: local changes to %s would be lost; pass Force to overwrite",
		snap.Name, w.Dir, strings.Join(paths, ", "))
}

// checkoutTree moves the working tree from the from tree to the to tree with a
// two-way merge in a private index, which updates and removes exactly the
// files that differ and refuses to clobber anything outside from.
func (w *Workspace) checkoutTree(ctx context.Context, from, to string) error {
	return withTempIndex(ctx, w.Dir, func(git gitCmd, _ string) error {
		if _, err := git.output(ctx, "read-tree", from); err != nil {
			return err
		}
		if _, err := git.output(ctx, "update-index", "-q", "--refresh"); err != nil {
			return err
		}
		_, err := git.output(ctx, "read-tree", "-m", "-u", from, to)
		return err
	})
}
