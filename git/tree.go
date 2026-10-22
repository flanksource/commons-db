package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
)

// TreeOptions selects an immutable commit from one cached remote repository or
// one existing local repository. Directory accepts working and bare repositories.
type TreeOptions struct {
	Repository *Repository
	Directory  string
	Revision   string
}

// TreeInfo identifies a tree's immutable commit and original object database.
type TreeInfo struct {
	SHA          string
	GitDirectory string
}

// Tree is a read-only filesystem over one immutable Git commit.
type Tree struct {
	ctx      context.Context
	info     TreeInfo
	dir      string
	env      []string
	cleanup  func() error
	release  func()
	mu       sync.Mutex
	closed   bool
	complete bool
	stream   *objectStream
	nodes    map[string]treeNode
	children map[string][]string
}

// OpenTree captures the revision without writing source files or leasing a
// worktree. Close releases its reader slot and any private local Git metadata.
func (c *Cache) OpenTree(ctx context.Context, options TreeOptions) (result *Tree, err error) {
	if c == nil || c.Root == "" || options.Revision == "" {
		return nil, fmt.Errorf("%w: cache root and tree revision are required", ErrInvalidSource)
	}
	if (options.Repository == nil && options.Directory == "") || (options.Repository != nil && options.Directory != "") {
		return nil, fmt.Errorf("%w: choose one repository or local directory", ErrInvalidSource)
	}
	tree := &Tree{ctx: ctx}
	defer func() {
		if err != nil {
			err = errors.Join(err, tree.Close())
		}
	}()
	if options.Directory != "" {
		err = tree.openLocal(c.Root, options)
	} else {
		var commits []string
		tree.dir, commits, err = c.prepareReadRefs(ctx, options.Repository, refReadOptions{Refs: []refOn{{options.Repository, options.Revision}}})
		if err == nil {
			tree.info = TreeInfo{SHA: commits[0], GitDirectory: tree.dir}
			tree.release, err = acquireRepositoryReader(ctx, tree.dir)
		}
	}
	if err != nil {
		return nil, err
	}
	return tree, nil
}

func (t *Tree) openLocal(cacheRoot string, options TreeOptions) error {
	directory, err := run(t.ctx, command{dir: options.Directory, args: []string{"rev-parse", "--absolute-git-dir"}})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotRepository, err)
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	t.info.GitDirectory = directory
	t.release, err = acquireRepositoryReader(t.ctx, directory)
	if err != nil {
		return err
	}
	source := &PreparedSource{info: SourceInfo{Kind: SourceCommit, RepositoryRoot: directory}}
	t.cleanup = source.Release
	if err := source.initialize(t.ctx, cacheRoot); err != nil {
		return err
	}
	t.info.SHA, err = source.resolveCommit(t.ctx, options.Revision)
	t.dir, t.env = source.gitDir, source.env
	return err
}

func (t *Tree) Info() TreeInfo { return t.info }

// Metadata describes the captured commit without inspecting a worktree or
// traversing its ancestry. Dirty is empty because the tree is immutable.
func (t *Tree) Metadata(options InspectOptions) (Metadata, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return Metadata{}, fs.ErrClosed
	}
	last, err := run(t.ctx, command{dir: t.dir, env: t.env, args: []string{"show", "--no-patch", "--format=%an%x00%cI%x00%s", "--end-of-options", t.info.SHA}})
	if err != nil {
		return Metadata{}, err
	}
	commit, err := parseLastCommit(last)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Repository: sanitizeRepository(cmp.Or(options.RepositoryHint, t.info.GitDirectory)),
		Branch: cmp.Or(options.BranchHint, "detached"), SHA: t.info.SHA, LastCommit: commit}, nil
}

func (t *Tree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var err error
	if !t.closed {
		t.closed = true
		if t.stream != nil {
			err = t.stream.close()
		}
		if t.release != nil {
			t.release()
			t.release = nil
		}
	}
	if t.cleanup != nil {
		cleanupErr := t.cleanup()
		err = errors.Join(err, cleanupErr)
		if cleanupErr == nil {
			t.cleanup = nil
		}
	}
	return err
}

func (t *Tree) ReadFile(name string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	node, err := t.node(name)
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	if !node.mode.IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fmt.Errorf("%w: mode %s", ErrUnsupportedSource, node.mode)}
	}
	if t.stream == nil {
		t.stream, err = openObjectStream(t.ctx, command{dir: t.dir, env: t.env})
		if err != nil {
			return nil, &fs.PathError{Op: "read", Path: name, Err: err}
		}
	}
	var content []byte
	err = t.stream.read(node.object, func(blob batchBlob, reader io.Reader) error {
		if blob.Missing {
			return fmt.Errorf("source object %s for %q is missing", node.object, name)
		}
		var err error
		content, err = io.ReadAll(reader)
		return err
	})
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return content, nil
}
