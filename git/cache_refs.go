package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func recordCacheRef(ctx context.Context, mirror string, repo *Repository, ref Ref) error {
	if ref.Kind != RefBranch {
		return nil
	}
	_, err := run(ctx, command{dir: mirror, args: []string{"update-ref", cacheRefPrefix + repo.URL.CacheKey() + "/heads/" + ref.Name, ref.SHA}})
	return err
}

// prepareOptions is one commit of Repo to make present, with its complete
// history, in the mirror and pin.
type prepareOptions struct {
	Repo *Repository
	Ref  Ref
}

// prepareRef makes the commit present with its complete history, retains and
// advertises it in the pool, and pins it. Every cached commit is complete: no
// read creates a shallow mirror.
func (m mirrorPaths) prepareRef(ctx context.Context, o prepareOptions) error {
	ref := o.Ref
	if pinned, err := m.usePinned([]string{ref.SHA}); err != nil {
		return err
	} else if pinned {
		return recordCacheRef(ctx, m.git, o.Repo, ref)
	}
	if err := m.fetchCommit(ctx, o); err != nil {
		return err
	}
	if err := m.retain(ctx, []string{ref.SHA}, true); err != nil {
		return err
	}
	if err := recordCacheRef(ctx, m.git, o.Repo, ref); err != nil {
		return err
	}
	return m.pin(ctx, []string{ref.SHA})
}

// fetchCommit fetches the commit with its complete history unless the mirror
// or its pool already holds it: into the pool when there is one, so the
// objects are stored once, into the mirror otherwise.
func (m mirrorPaths) fetchCommit(ctx context.Context, o prepareOptions) error {
	if err := requireComplete(m.git); err != nil {
		return err
	}
	if hasCommit(ctx, m.git, o.Ref.SHA) {
		return nil
	}
	if m.pool != "" {
		return m.fetchIntoPool(ctx, o)
	}
	sha, err := fetch(ctx, fetchOptions{Dir: m.git, Owner: o.Repo, Repo: o.Repo, Ref: o.Ref.bySHA()})
	if err != nil {
		return err
	}
	if sha != o.Ref.SHA {
		return fmt.Errorf("fetch returned %s, expected %s", sha, o.Ref.SHA)
	}
	return nil
}

// poolHoldsRepository reports whether the pool advertises a commit of this
// mirror's repository.
func (m mirrorPaths) poolHoldsRepository(ctx context.Context) (bool, error) {
	tips, err := run(ctx, command{dir: m.pool, args: []string{"for-each-ref", "--count=1", "--format=%(refname)", poolTips + m.repoKey() + "/"}})
	return tips != "", err
}

// fetchIntoPool fetches the commit with its complete history straight into
// the organization pool, so the objects are stored once and the mirror never
// holds a copy: the pool's refs of every repository it serves are the
// negotiation's haves. The fetch lock serializes the first fetch of a
// repository (the pool advertises nothing of it yet) against every other
// fetch into the pool, so a concurrent first fetch of a related repository
// waits and then negotiates against what this one brought; fetches of
// repositories the pool already holds share the lock.
func (m mirrorPaths) fetchIntoPool(ctx context.Context, o prepareOptions) (err error) {
	holds, err := m.poolHoldsRepository(ctx)
	if err != nil {
		return err
	}
	fetchLease := m.locks.ReadWriteLock(m.poolLease("fetch"), m.holder)
	acquireFetch := fetchLease.RLock
	if !holds {
		acquireFetch = fetchLease.Lock
	}
	ctx, release, err := holdLease(ctx, "git object pool "+m.poolName()+" fetch lease", acquireFetch)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	if err := requireComplete(m.pool); err != nil {
		return err
	}
	sha := o.Ref.SHA
	if hasCommit(ctx, m.pool, sha) {
		return nil
	}
	if err := writeStamp(m.pool, m.repoKey(), time.Now()); err != nil {
		return err
	}
	destination := poolRetained + m.repoKey() + "/" + sha
	fetched, err := fetch(ctx, fetchOptions{Dir: m.pool, Owner: o.Repo, Repo: o.Repo, Ref: o.Ref.bySHA(),
		Refspecs: []string{"+" + sha + ":" + destination}, Destination: destination, Pool: m.poolName()})
	if err != nil {
		return err
	}
	if fetched != sha {
		return fmt.Errorf("fetch returned %s, expected %s", fetched, sha)
	}
	return nil
}

// requireComplete refuses a shallow repository: every mirror trusts a commit
// it or its pool holds to carry its complete history.
func requireComplete(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "shallow")); err == nil {
		return fmt.Errorf("git repository %s is shallow; the cache holds complete histories only", dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// bySHA is the ref fetched by its commit SHA, still named as requested.
func (ref Ref) bySHA() Ref {
	return Ref{Name: ref.Name, FullName: ref.SHA, SHA: ref.SHA, Kind: RefSHA}
}
