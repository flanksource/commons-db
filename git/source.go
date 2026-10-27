package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

type SourceKind string

const (
	SourceWorktree SourceKind = "worktree"
	SourceCommit   SourceKind = "commit"
	SourceStaged   SourceKind = "staged"
)

var (
	ErrInvalidSource     = errors.New("invalid source request")
	ErrRevisionNotFound  = errors.New("revision does not resolve to one commit")
	ErrUnmergedIndex     = errors.New("unmerged index")
	ErrSourceChanged     = errors.New("source changed while capturing")
	ErrUnsupportedSource = errors.New("unsupported source")
)

// SourceOptions selects a local repository source. Directory may be any directory inside its checkout.
type SourceOptions struct {
	Directory string
	Kind      SourceKind
	// Revision is required only for SourceCommit: one commit, A..B, or A...B.
	Revision string
}

// SourceInfo separates the source's temporary read location from its original checkout and Git identity.
type SourceInfo struct {
	Kind           SourceKind
	RepositoryRoot string
	Dir            string
	// BaseCommit is empty for an initial commit or an unborn checkout.
	BaseCommit string
	// TargetCommit is populated only for SourceCommit.
	TargetCommit string
	// TreeID is the native Git tree ID for commit and staged sources; live worktrees have none.
	TreeID string
}

// SourceChange uses a slash-separated, repository-relative literal path. Renames are a deletion and addition.
type SourceChange struct {
	Path   string
	Status FileStatus
}

// PreparedSource owns an isolated tree, or borrows the live working tree for SourceWorktree.
type PreparedSource struct {
	info    SourceInfo
	changes []SourceChange
	scratch string
	gitDir  string
	env     []string
	mu      sync.Mutex
}

// PrepareSource captures a local source and its changes. Commit and staged sources contain raw blob bytes.
func (c *Cache) PrepareSource(ctx context.Context, options SourceOptions) (source *PreparedSource, err error) {
	if c == nil || c.Root == "" || options.Directory == "" || !validSourceOptions(options) {
		return nil, ErrInvalidSource
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := sourceRepository(ctx, options.Directory)
	if err != nil {
		return nil, err
	}
	source = &PreparedSource{info: SourceInfo{Kind: options.Kind, RepositoryRoot: root, Dir: root}, changes: []SourceChange{}}
	if options.Kind == SourceWorktree {
		source.info.BaseCommit, err = sourceHead(ctx, root)
		if err == nil {
			source.changes, err = sourceWorkingChanges(ctx, source.info)
		}
		if err != nil {
			return nil, fmt.Errorf("prepare %s source in %q: %w", options.Kind, root, err)
		}
		return source, err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("prepare %s source in %q: %w", options.Kind, root, errors.Join(err, source.Release()))
			source = nil
		}
	}()
	if err = source.initialize(ctx, c.Root); err != nil {
		return source, err
	}
	if err = source.pinHead(ctx); err != nil {
		return source, err
	}
	if options.Kind == SourceStaged {
		err = source.captureIndex(ctx)
	} else {
		err = source.resolveRevision(ctx, options.Revision)
	}
	if err != nil {
		return source, err
	}
	if source.changes, err = source.treeChanges(ctx); err != nil {
		return source, err
	}
	if err = source.materialize(ctx); err != nil {
		return source, err
	}
	return source, nil
}

func validSourceOptions(options SourceOptions) bool {
	switch options.Kind {
	case SourceWorktree, SourceStaged:
		return options.Revision == ""
	case SourceCommit:
		return options.Revision != ""
	default:
		return false
	}
}

func sourceRepository(ctx context.Context, directory string) (string, error) {
	bare, err := run(ctx, command{dir: directory, args: []string{"rev-parse", "--is-bare-repository"}})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotRepository, err)
	}
	if bare == "true" {
		return "", fmt.Errorf("%w: bare repository %q", ErrUnsupportedSource, directory)
	}
	root, err := run(ctx, command{dir: directory, args: []string{"rev-parse", "--show-toplevel"}})
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

func (s *PreparedSource) Info() SourceInfo { return s.info }

// Changes returns an independent, path-sorted selection, including deletions. An empty selection is non-nil.
func (s *PreparedSource) Changes() []SourceChange { return slices.Clone(s.changes) }

// Release removes only this source's owned resources, independently of the request context.
func (s *PreparedSource) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scratch == "" {
		return nil
	}
	if err := os.RemoveAll(s.scratch); err != nil {
		return err
	}
	s.scratch = ""
	return nil
}

func (s *PreparedSource) captureIndex(ctx context.Context) error {
	root := s.info.RepositoryRoot
	index, err := run(ctx, command{dir: root, args: []string{"rev-parse", "--path-format=absolute", "--git-path", "index"}})
	if err != nil {
		return err
	}
	private := filepath.Join(s.gitDir, "index")
	content, err := os.ReadFile(index)
	if errors.Is(err, os.ErrNotExist) {
		if _, err = s.git(ctx, "read-tree", "--empty"); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		if err := os.WriteFile(private, content, 0o600); err != nil {
			return err
		}
		shared, err := run(ctx, command{dir: root, env: append(baseEnv(), "GIT_INDEX_FILE="+private),
			args: []string{"--no-optional-locks", "rev-parse", "--shared-index-path"}})
		if err != nil {
			return err
		}
		if shared != "" {
			if !filepath.IsAbs(shared) {
				shared = filepath.Join(root, shared)
			}
			backing, err := os.ReadFile(shared)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(s.gitDir, filepath.Base(shared)), backing, 0o600); err != nil {
				return err
			}
		}
	}
	unmerged, err := s.git(ctx, "ls-files", "--unmerged", "-z")
	if err != nil {
		return err
	}
	if unmerged != "" {
		return ErrUnmergedIndex
	}
	if _, err := s.git(ctx, "-c", "core.splitIndex=false", "update-index", "--no-split-index"); err != nil {
		return err
	}
	s.info.TreeID, err = s.git(ctx, "write-tree")
	if err != nil {
		return err
	}
	head, err := sourceHead(ctx, root)
	if err != nil {
		return err
	}
	if head != s.info.BaseCommit {
		return ErrSourceChanged
	}
	return nil
}
