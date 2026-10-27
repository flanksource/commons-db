package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
)

// lsRemoteLocal lists a local repository's refs as git advertises them to a
// fetch (git ls-remote --symref), so a detached HEAD, linked worktrees and
// any number of refs read as they do natively.
func lsRemoteLocal(ctx context.Context, path string) (RemoteRefs, error) {
	out, err := run(ctx, command{dir: path, args: []string{"ls-remote", "--symref", "--end-of-options", path}})
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %w", path, err)
	}
	list, err := parseLsRemote(out)
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %w", path, err)
	}
	return refsFromList(list), nil
}

func parseLsRemote(out string) ([]*plumbing.Reference, error) {
	var list []*plumbing.Reference
	symbolic := map[string]bool{}
	for _, line := range lines(out) {
		value, name, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected ls-remote line %q", line)
		}
		if target, isSymref := strings.CutPrefix(value, "ref: "); isSymref {
			list = append(list, plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target)))
			symbolic[name] = true
			continue
		}
		if symbolic[name] {
			continue
		}
		if !fullSHAPattern.MatchString(value) {
			return nil, fmt.Errorf("unexpected ls-remote object %q for %s", value, name)
		}
		list = append(list, plumbing.NewHashReference(plumbing.ReferenceName(name), plumbing.NewHash(value)))
	}
	return list, nil
}

// localReader is the private bare repository a local repository is read
// through, at <Root>/<CacheKey>/local.git: it has no refs and borrows the
// local repository's objects (objects/info/alternates) and shallow file, so a
// read copies nothing and sees none of the local repository's config.
type localReader struct {
	git          string
	source       string
	gitDirectory string
	objects      string
	verified     sync.Map
}

// localReaders holds one localReader per scratch directory per process.
var localReaders sync.Map

func (c *Cache) localReader(ctx context.Context, repo *Repository) (*localReader, error) {
	paths, err := c.mirrorPaths(repo)
	if err != nil {
		return nil, err
	}
	scratch, err := filepath.Abs(filepath.Join(paths.dir, "local.git"))
	if err != nil {
		return nil, err
	}
	if known, ok := localReaders.Load(scratch); ok {
		return known.(*localReader), nil
	}
	source, _ := repo.URL.LocalPath()
	reader := &localReader{git: scratch, source: source}
	if err := reader.create(ctx, paths.lock); err != nil {
		return nil, err
	}
	known, _ := localReaders.LoadOrStore(scratch, reader)
	return known.(*localReader), nil
}

// create initializes the scratch repository once and points it at the local
// repository's current object directory, under the exclusive mirror lock.
func (r *localReader) create(ctx context.Context, lockPath string) (err error) {
	out, err := run(ctx, command{dir: r.source, args: []string{"rev-parse", "--show-object-format", "--absolute-git-dir",
		"--path-format=absolute", "--git-path", "objects", "--git-path", "shallow"}})
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotRepository, r.source, err)
	}
	located := strings.Split(out, "\n")
	if len(located) != 4 {
		return fmt.Errorf("unexpected rev-parse output %q for %s", out, r.source)
	}
	r.gitDirectory, r.objects = located[1], located[2]
	lock, err := acquire(ctx, lockPath, "mirror lock", exclusiveLock)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	if _, err := os.Stat(filepath.Join(r.git, "HEAD")); errors.Is(err, fs.ErrNotExist) {
		if _, err := run(ctx, command{dir: filepath.Dir(r.git), args: []string{"init", "--quiet", "--bare", "--template=",
			"--object-format=" + located[0], r.git}}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return r.borrow(located[2], located[3])
}

// borrow writes the alternates file and links the shallow file of the local
// repository; a dangling shallow link reads as a complete history.
func (r *localReader) borrow(objects, shallow string) error {
	info := filepath.Join(r.git, "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", info, err)
	}
	alternates := filepath.Join(info, "alternates")
	if current, err := os.ReadFile(alternates); err != nil || string(current) != objects+"\n" {
		staged := alternates + ".tmp"
		if err := os.WriteFile(staged, []byte(objects+"\n"), 0o644); err != nil {
			return fmt.Errorf("write alternates of %s: %w", r.git, err)
		}
		if err := os.Rename(staged, alternates); err != nil {
			return fmt.Errorf("write alternates of %s: %w", r.git, err)
		}
	}
	link := filepath.Join(r.git, "shallow")
	if target, err := os.Readlink(link); err == nil && target == shallow {
		return nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("link shallow file of %s: %w", r.git, err)
	}
	if err := os.Symlink(shallow, link); err != nil {
		return fmt.Errorf("link shallow file of %s: %w", r.git, err)
	}
	return nil
}

// requirePresent fails with ErrNotRepository once the local repository's
// object directory is gone (moved or deleted), where git would read every
// borrowed object as missing. It forgets the reader, so a repository back at
// its path is borrowed again.
func (r *localReader) requirePresent() error {
	if _, err := os.Stat(r.objects); err != nil {
		localReaders.Delete(r.git)
		return fmt.Errorf("%w: local repository %s: object directory %s: %w", ErrNotRepository, r.source, r.objects, err)
	}
	return nil
}

// requireCommits fails with ErrRevisionNotFound unless every SHA is a commit
// of the local repository; commits seen once cost no process again.
func (r *localReader) requireCommits(ctx context.Context, shas []string) error {
	var unverified []string
	for _, sha := range shas {
		if _, known := r.verified.Load(sha); !known {
			unverified = append(unverified, sha)
		}
	}
	if len(unverified) == 0 {
		return nil
	}
	out, err := run(ctx, command{dir: r.git, stdin: []byte(strings.Join(unverified, "^{commit}\n") + "^{commit}\n"),
		args: []string{"cat-file", "--batch-check=%(objectname)"}})
	if err != nil {
		return err
	}
	found := lines(out)
	for i, sha := range unverified {
		if i >= len(found) || found[i] != sha {
			return fmt.Errorf("%w: commit %s is not in %s", ErrRevisionNotFound, sha, r.source)
		}
		r.verified.Store(sha, true)
	}
	return nil
}

// readsInPlace reports whether every ref is on repo and repo is local, so a
// read needs neither a fetch nor the mirror.
func readsInPlace(repo *Repository, refs []refOn) bool {
	if repo == nil {
		return false
	}
	if _, local := repo.URL.LocalPath(); !local {
		return false
	}
	for _, ref := range refs {
		if ref.repo == nil || ref.repo.URL.Clone != repo.URL.Clone {
			return false
		}
	}
	return true
}

// readRefs runs a read at options.Refs: in place for a local repository,
// otherwise in the mirror (withRefs). Writes always go through withRefs.
func (c *Cache) readRefs(ctx context.Context, repo *Repository, options refReadOptions, fn func(dir string, shas []string) error) error {
	if !readsInPlace(repo, options.Refs) {
		return c.withRefs(ctx, repo, options, fn)
	}
	reader, shas, err := c.prepareLocalRefs(ctx, repo, options)
	if err != nil {
		return err
	}
	return readRepository(ctx, reader.git, func() error { return fn(reader.git, shas) })
}

// prepareLocalRefs resolves options.Refs on a local repository (natively, a
// full SHA without listing) and checks their commits are present.
func (c *Cache) prepareLocalRefs(ctx context.Context, repo *Repository, options refReadOptions) (*localReader, []string, error) {
	reader, err := c.localReader(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	if err := reader.requirePresent(); err != nil {
		return nil, nil, err
	}
	resolved, err := resolveReadRefs(ctx, options.Refs)
	if err != nil {
		return nil, nil, err
	}
	shas := make([]string, len(resolved))
	for i, ref := range resolved {
		shas[i] = ref.SHA
	}
	return reader, shas, reader.requireCommits(ctx, shas)
}
