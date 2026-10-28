package workspace

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// PushOptions configures Push. Remote is a remote name, URL or repository
// path; Ref is the full ref written there (refs/heads/x, refs/snapshots/x).
type PushOptions struct {
	Remote string
	Ref    string
	// Source is the commit-ish pushed (default HEAD), e.g. a snapshot commit.
	Source string
	// Lease is the SHA Ref must still hold on the remote; set, the push may
	// rewrite history, but only over exactly that SHA (--force-with-lease).
	Lease string
	// From is the lower bound of the outgoing range the gates inspect. Unset it
	// is Lease, else Ref's current tip on Remote; when neither is known
	// locally, the gates see the whole of Source.
	From          string
	Atomic        bool
	ServerOptions []string
	Gates         []Gate
}

// Push runs the gates over everything the push would send, then pushes. A
// refusing gate sends nothing.
func (w *Workspace) Push(ctx context.Context, opts PushOptions) error {
	if opts.Remote == "" {
		return errors.New("workspace: push needs a remote")
	}
	if !strings.HasPrefix(opts.Ref, "refs/") {
		return fmt.Errorf("workspace: push needs a full ref name (refs/...), got %q", opts.Ref)
	}
	if _, err := gitOutput(ctx, w.Dir, "check-ref-format", opts.Ref); err != nil {
		return fmt.Errorf("workspace: invalid push ref %q: %w", opts.Ref, err)
	}
	source := opts.Source
	if source == "" {
		source = "HEAD"
	}
	to, err := gitString(ctx, w.Dir, "rev-parse", "--verify", "--end-of-options", source+"^{commit}")
	if err != nil {
		return err
	}
	from, err := w.pushBase(ctx, opts)
	if err != nil {
		return err
	}
	check, err := w.outgoing(ctx, from, to)
	if err != nil {
		return err
	}
	if err := w.runGates(ctx, check, opts.Gates); err != nil {
		return err
	}

	args := []string{"push", "--porcelain"}
	if opts.Atomic {
		args = append(args, "--atomic")
	}
	for _, option := range opts.ServerOptions {
		args = append(args, "--push-option="+option)
	}
	if opts.Lease != "" {
		args = append(args, fmt.Sprintf("--force-with-lease=%s:%s", opts.Ref, opts.Lease))
	}
	_, err = gitOutput(ctx, w.Dir, append(args, "--", opts.Remote, to+":"+opts.Ref)...)
	return err
}

// pushBase resolves the commit the outgoing range starts after. A remote tip
// this repository has never fetched cannot bound a range, so the gates then
// inspect everything Source carries — more work, never less checking.
func (w *Workspace) pushBase(ctx context.Context, opts PushOptions) (string, error) {
	base := opts.From
	if base == "" {
		base = opts.Lease
	}
	if base == "" {
		out, err := gitString(ctx, w.Dir, "ls-remote", "--", opts.Remote, opts.Ref)
		if err != nil {
			return "", err
		}
		base, _, _ = strings.Cut(out, "\t")
	}
	if base == "" {
		return "", nil
	}
	resolved, err := gitCmd{Dir: w.Dir}.string(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}")
	if err != nil {
		if opts.From != "" {
			return "", fmt.Errorf("workspace: push From %q is not a commit here: %w", opts.From, err)
		}
		return "", nil
	}
	return resolved, nil
}

// outgoing describes what pushing to over from sends: every path a commit in
// the range touched, and the net change.
func (w *Workspace) outgoing(ctx context.Context, from, to string) (Check, error) {
	args := []string{"log", "--format=", "--name-only", "--no-renames", "-z", to}
	if from != "" {
		args = append(args, "^"+from)
	}
	out, err := gitOutput(ctx, w.Dir, args...)
	if err != nil {
		return Check{}, err
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, path := range splitNUL(out) {
		path = strings.TrimPrefix(path, "\n")
		if _, ok := seen[path]; path == "" || ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes, err := w.Outgoing(ctx, from, to)
	if err != nil {
		return Check{}, err
	}
	return Check{Phase: PhasePush, From: from, To: to, Paths: paths, Changes: changes}, nil
}
