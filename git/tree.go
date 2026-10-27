package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	listed   map[string]bool
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
		err = tree.openRepository(c, options.Repository, options.Revision)
	}
	if err != nil {
		return nil, err
	}
	return tree, nil
}

// openRepository reads a local repository in place (readRefs) and any other
// through the pinned mirror.
func (t *Tree) openRepository(c *Cache, repo *Repository, revision string) error {
	refs := refReadOptions{Refs: []refOn{{repo, revision}}}
	var commits []string
	var err error
	if readsInPlace(repo, refs.Refs) {
		var reader *localReader
		if reader, commits, err = c.prepareLocalRefs(t.ctx, repo, refs); err != nil {
			return err
		}
		t.dir, t.info = reader.git, TreeInfo{SHA: commits[0], GitDirectory: reader.gitDirectory}
	} else {
		if t.dir, commits, err = c.prepareReadRefs(t.ctx, repo, refs); err != nil {
			return err
		}
		t.info = TreeInfo{SHA: commits[0], GitDirectory: t.dir}
	}
	t.release, err = acquireRepositoryReader(t.ctx, t.dir)
	return err
}

func (t *Tree) openLocal(cacheRoot string, options TreeOptions) error {
	local, err := openLocalRepository(t.ctx, localRepositoryOptions{CacheRoot: cacheRoot, Directory: options.Directory, Revisions: []string{options.Revision}})
	if local != nil {
		t.info.GitDirectory, t.release, t.cleanup = local.gitDirectory, local.release, local.source.Release
	}
	if err != nil {
		return err
	}
	t.info.SHA, t.dir, t.env = local.commits[0], local.source.gitDir, local.source.env
	return nil
}

type localRepositoryOptions struct {
	CacheRoot string
	Directory string
	Revisions []string
}

// localRepository is a private scratch repository reading a local
// repository's objects through alternates, holding a reader slot on it.
type localRepository struct {
	gitDirectory string
	source       *PreparedSource
	commits      []string
	release      func()
}

// close releases what openLocalRepository acquired.
func (l *localRepository) close() error {
	l.release()
	return l.source.Release()
}

// openLocalRepository resolves options.Revisions in the local repository and
// opens a scratch repository whose HEAD is the first of them; none of the
// local repository's refs are copied. On error, a non-nil result still owns
// what it acquired.
func openLocalRepository(ctx context.Context, options localRepositoryOptions) (*localRepository, error) {
	directory, err := run(ctx, command{dir: options.Directory, args: []string{"rev-parse", "--absolute-git-dir"}})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotRepository, err)
	}
	if directory, err = filepath.EvalSymlinks(directory); err != nil {
		return nil, err
	}
	release, err := acquireRepositoryReader(ctx, directory)
	if err != nil {
		return nil, err
	}
	local := &localRepository{gitDirectory: directory, release: release,
		source: &PreparedSource{info: SourceInfo{Kind: SourceCommit, RepositoryRoot: directory}}}
	if err := local.source.initialize(ctx, options.CacheRoot); err != nil {
		return local, err
	}
	for _, revision := range options.Revisions {
		commit, err := local.source.resolveCommit(ctx, revision)
		if err != nil {
			return local, err
		}
		local.commits = append(local.commits, commit)
	}
	return local, os.WriteFile(filepath.Join(local.source.gitDir, "HEAD"), []byte(local.commits[0]+"\n"), 0o600)
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
	if err := t.openStream(); err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
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
	node.size, node.sized = int64(len(content)), true
	t.nodes[name] = node
	return content, nil
}

func (t *Tree) openStream() (err error) {
	if t.stream == nil {
		t.stream, err = openObjectStream(t.ctx, command{dir: t.dir, env: t.env})
	}
	return err
}
