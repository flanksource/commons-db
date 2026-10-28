// Package workspace prepares the directory a run works in from a source git
// checkout — the checkout itself at HEAD, a clean worktree, or a worktree
// carrying the source's uncommitted work — and syncs folder state between
// workspaces as git snapshots.
//
// A snapshot is a commit of the working state (tracked and untracked files,
// never gitignored ones) stored under refs/snapshots/<name>. It is built in a
// private index, so taking one never touches HEAD, the index or the stash.
// Pushing and pulling snapshots uses git itself as the folder sync protocol.
package workspace

import (
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/context"
)

// Kind names how a workspace relates to its source checkout.
type Kind string

const (
	// KindHead is the source checkout itself, used in place.
	KindHead Kind = "head"
	// KindClean is a new git worktree at Base with none of the source's
	// uncommitted work.
	KindClean Kind = "clean"
	// KindDirty is a new git worktree that also carries the source's staged,
	// unstaged and untracked work.
	KindDirty Kind = "dirty"
)

// Options configures Prepare. Source is any directory inside the source
// checkout; every other field applies only to worktree kinds.
type Options struct {
	Source string
	Kind   Kind

	// BaseDir holds generated worktrees (BaseDir/worktrees/<branch>-<uuid>)
	// unless Path names the worktree directory explicitly.
	BaseDir string
	Path    string
	// Branch is the branch the worktree creates, or with Existing the branch it
	// checks out as-is. Unset, a new branch is named <Prefix>/<uuid>.
	Branch   string
	Prefix   string
	Existing bool
	// Base is the commit-ish a new branch starts from (default: the source's
	// HEAD).
	Base string
	// Ignored copy-on-write clones the source's gitignored content
	// (node_modules/, build caches, .env) into the worktree. Cloned paths are
	// recorded in Workspace.Ignored and never enter a snapshot.
	Ignored bool
	// Keep leaves the worktree on disk when the workspace is released.
	Keep bool
}

// Workspace is a prepared directory and what it was prepared from.
type Workspace struct {
	Kind Kind
	// Source is the root of the source checkout.
	Source string
	// Dir is the root the run works in: Source for KindHead, else the worktree.
	Dir    string
	Branch string
	// Base is the commit Dir's HEAD pointed at when it was prepared.
	Base string
	// Carried lists the source's uncommitted paths copied in (KindDirty).
	Carried []string
	// Ignored lists the gitignored content cloned in from the source as
	// compact entries: a file, or a directory ending in "/". It is recorded in
	// the worktree's manifest, and IsExcluded keeps it out of every commit,
	// snapshot and push even where the worktree's branch does not ignore it.
	Ignored []string

	release func() error
}

// Release tears the workspace down: a worktree is removed unless kept, and the
// source checkout of a head workspace is never touched.
func (w *Workspace) Release() error {
	if w == nil || w.release == nil {
		return nil
	}
	release := w.release
	w.release = nil
	return release()
}

// Adapter prepares one kind of workspace.
type Adapter interface {
	Prepare(ctx context.Context, source string, opts Options) (*Workspace, error)
}

var adapters = map[Kind]Adapter{
	KindHead:  headAdapter{},
	KindClean: worktreeAdapter{kind: KindClean},
	KindDirty: worktreeAdapter{kind: KindDirty},
}

// Prepare resolves opts.Source to its checkout root and prepares the workspace
// opts.Kind names.
func Prepare(ctx context.Context, opts Options) (*Workspace, error) {
	adapter, ok := adapters[opts.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown workspace kind %q (want %s, %s or %s)", opts.Kind, KindHead, KindClean, KindDirty)
	}
	if opts.Source == "" {
		return nil, errors.New("workspace: Source is required")
	}
	source, err := checkoutRoot(ctx, opts.Source)
	if err != nil {
		return nil, err
	}
	return adapter.Prepare(ctx, source, opts)
}

type contextKey struct{}

// NewContext returns ctx carrying ws, for code that resolves the directory it
// works in from the context.
func NewContext(ctx context.Context, ws *Workspace) context.Context {
	return ctx.WithValue(contextKey{}, ws)
}

// FromContext returns the workspace NewContext attached to ctx.
func FromContext(ctx context.Context) (*Workspace, bool) {
	ws, ok := ctx.Value(contextKey{}).(*Workspace)
	return ws, ok && ws != nil
}
