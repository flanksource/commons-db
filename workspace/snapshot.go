package workspace

import (
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const snapshotRefPrefix = "refs/snapshots/"

// snapshotIdentity signs snapshot commits, so taking one never depends on (or
// leaks) the user's git identity.
var snapshotIdentity = []string{
	"GIT_AUTHOR_NAME=workspace", "GIT_AUTHOR_EMAIL=workspace@localhost",
	"GIT_COMMITTER_NAME=workspace", "GIT_COMMITTER_EMAIL=workspace@localhost",
}

// Snapshot is a commit of a workspace's working state. Its first parent is
// Base, the commit the state is relative to; its second parent, when set, is
// the Previous snapshot under the same name, so successive snapshots form a
// chain that fast-forwards.
type Snapshot struct {
	Name     string
	Ref      string
	Commit   string
	Tree     string
	Base     string
	Previous string
}

// Snapshot records the working state — tracked and untracked files, never
// gitignored ones or the paths in Ignored — as refs/snapshots/<name>. HEAD,
// the index and the working tree are left untouched. An unchanged state
// returns the existing snapshot instead of cutting a new one.
func (w *Workspace) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	ref, err := snapshotRef(ctx, w.Dir, name)
	if err != nil {
		return nil, err
	}
	tree, err := w.workingTree(ctx)
	if err != nil {
		return nil, err
	}
	base, err := gitString(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	args := []string{"commit-tree", tree, "-p", base, "-m", "workspace snapshot " + name}
	oldValue := strings.Repeat("0", 40)
	previous, err := optionalCommit(ctx, w.Dir, ref)
	if err != nil {
		return nil, err
	}
	if previous != "" {
		last, err := readSnapshot(ctx, w.Dir, name, previous)
		if err != nil {
			return nil, err
		}
		if last.Tree == tree && last.Base == base {
			return last, nil
		}
		args = append(args, "-p", previous)
		oldValue = previous
	}
	commit, err := gitCmd{Dir: w.Dir, Env: snapshotIdentity}.string(ctx, args...)
	if err != nil {
		return nil, err
	}
	if _, err := gitOutput(ctx, w.Dir, "update-ref", ref, commit, oldValue); err != nil {
		return nil, err
	}
	return &Snapshot{Name: name, Ref: ref, Commit: commit, Tree: tree, Base: base, Previous: previous}, nil
}

// workingTree writes the working state as a tree object through a private
// index seeded from the real one, whose stat cache spares rehashing every
// tracked file.
func (w *Workspace) workingTree(ctx context.Context) (string, error) {
	realIndex, err := gitString(ctx, w.Dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	var tree string
	err = withTempIndex(ctx, w.Dir, func(git gitCmd, index string) error {
		if _, statErr := os.Stat(realIndex); statErr == nil {
			if _, _, err := copyPath(realIndex, index); err != nil {
				return fmt.Errorf("seed snapshot index: %w", err)
			}
		} else if _, err := git.output(ctx, "read-tree", "HEAD"); err != nil {
			return err
		}
		if _, err := git.output(ctx, "add", "-A"); err != nil {
			return err
		}
		// A path the source ignores may be visible to this worktree's branch;
		// cloned ignored content must never enter a snapshot regardless.
		if len(w.Ignored) > 0 {
			exclude := git
			exclude.Stdin = []byte(strings.Join(w.Ignored, "\x00"))
			if _, err := exclude.output(ctx, "rm", "--cached", "-r", "-q", "--ignore-unmatch",
				"--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
				return err
			}
		}
		var err error
		tree, err = git.string(ctx, "write-tree")
		return err
	})
	return tree, err
}

func snapshotRef(ctx context.Context, dir, name string) (string, error) {
	ref := snapshotRefPrefix + name
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("workspace: invalid snapshot name %q", name)
	}
	if _, err := gitOutput(ctx, dir, "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("workspace: invalid snapshot name %q: %w", name, err)
	}
	return ref, nil
}

// optionalCommit resolves the full ref name to the commit it points at, or ""
// when the ref does not exist. for-each-ref also matches refs below ref, so
// the name is compared exactly.
func optionalCommit(ctx context.Context, dir, ref string) (string, error) {
	out, err := gitOutput(ctx, dir, "for-each-ref", "--format=%(refname) %(objectname)", ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok && name == ref {
			return sha, nil
		}
	}
	return "", nil
}

func readSnapshot(ctx context.Context, dir, name, rev string) (*Snapshot, error) {
	out, err := gitString(ctx, dir, "log", "-1", "--format=%H%x00%T%x00%P", rev)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	if len(fields) != 3 {
		return nil, fmt.Errorf("workspace: read snapshot %s: unexpected log record %q", rev, out)
	}
	parents := strings.Fields(fields[2])
	if len(parents) == 0 || len(parents) > 2 {
		return nil, fmt.Errorf("workspace: %s (%s) is not a snapshot: it has %d parents", rev, fields[0], len(parents))
	}
	snap := &Snapshot{Name: name, Ref: snapshotRefPrefix + name, Commit: fields[0], Tree: fields[1], Base: parents[0]}
	if len(parents) == 2 {
		snap.Previous = parents[1]
	}
	return snap, nil
}
