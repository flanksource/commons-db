package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"
)

// storeRefPrefix namespaces, in a local mirror, every ref a Store hydrate
// brought in, so a hydrate never moves the mirror's own refs (and keeps the
// hydrated objects reachable through gc). Publish leaves it out, so refs never
// nest across round trips.
const storeRefPrefix = "refs/commons-store/"

// Store is a durable second tier under Cache: the local Root is ephemeral
// (an emptyDir), the store survives it on a shared volume (an EFS RWX PVC).
// A Cache with a Store hydrates a mirror from the store on a commit miss and
// fetches from the remote only what the store lacks; Cache.Fetch with Publish
// writes a mirror's new objects and refs back.
//
// Keys are the same RepoURL.CacheKey layout as Root (the directory of a
// repository mirror). Cache mirrors hold complete histories only; a shallow
// mirror is never published: a shallow history would truncate every other
// reader's log and merge base.
type Store interface {
	// Hydrate fetches what the store holds for key into the bare mirror; a
	// key the store has nothing for is no error.
	Hydrate(ctx context.Context, key, mirror string) error
	// Publish writes the mirror's objects and refs the store lacks for key.
	Publish(ctx context.Context, key, mirror string) error
	// Inventory describes every key the store holds, sorted by key.
	Inventory(ctx context.Context) ([]StoreEntry, error)
}

// StoreEntry is one repository in a Store. Bundles counts the pack store's
// bundles (zero for a mirror store); Published is the last publish.
type StoreEntry struct {
	Key       string     `json:"key"`
	SizeBytes int64      `json:"sizeBytes"`
	Bundles   int        `json:"bundles,omitempty"`
	Published *time.Time `json:"published,omitempty"`
}

// storeKeyOf is the Store key of the cache mirror at mirror, false for a
// repository that is not a cache mirror.
func storeKeyOf(mirror string) (string, bool) {
	return filepath.Base(filepath.Dir(mirror)), filepath.Base(mirror) == "mirror.git"
}

func validStoreKey(key string) error {
	if key == "" || key == "." || !filepath.IsLocal(key) || key != filepath.Base(key) {
		return fmt.Errorf("invalid git store key %q", key)
	}
	return nil
}

// hydrateMiss tries the store once if any SHA is absent from the mirror and
// its alternate. The caller holds the exclusive mirror lock.
func hydrateMiss(ctx context.Context, store Store, mirror string, shas ...string) error {
	if store == nil {
		return nil
	}
	miss := slices.IndexFunc(shas, func(sha string) bool { return !hasCommit(ctx, mirror, sha) })
	if miss < 0 {
		return nil
	}
	hydrated, err := hydrate(ctx, store, mirror)
	if err != nil || !hydrated {
		return err
	}
	sha := shas[miss]
	key, _ := storeKeyOf(mirror)
	log := logger.GetLogger("git").V(1)
	if hasCommit(ctx, mirror, sha) {
		log.Infof("git cache miss of %s in %s satisfied by the store", shortSHA(sha), key)
	} else {
		log.Infof("git cache miss of %s in %s not in the store; fetching from the remote", shortSHA(sha), key)
	}
	return nil
}

// hydrate hydrates the cache mirror from store, reporting false for no store
// or a repository that is not a cache mirror.
func hydrate(ctx context.Context, store Store, mirror string) (bool, error) {
	if store == nil {
		return false, nil
	}
	key, stored := storeKeyOf(mirror)
	if !stored {
		return false, nil
	}
	if err := store.Hydrate(ctx, key, mirror); err != nil {
		return false, fmt.Errorf("hydrate %s from git store: %w", key, err)
	}
	return true, nil
}

func shortSHA(sha string) string {
	return sha[:min(len(sha), 8)]
}

func isShallow(ctx context.Context, mirror string) (bool, error) {
	out, err := run(ctx, command{dir: mirror, args: []string{"rev-parse", "--is-shallow-repository"}})
	return out == "true", err
}

// publishMirror publishes the cache mirror at mirror to store, refusing a
// shallow one.
func publishMirror(ctx context.Context, store Store, mirror string) error {
	key, stored := storeKeyOf(mirror)
	if !stored {
		return fmt.Errorf("publish %s: only cache mirrors are published to the git store", mirror)
	}
	shallow, err := isShallow(ctx, mirror)
	if err != nil {
		return err
	}
	if shallow {
		return fmt.Errorf("publish %s: a shallow mirror is not published to the git store", key)
	}
	if err := store.Publish(ctx, key, mirror); err != nil {
		return fmt.Errorf("publish %s to git store: %w", key, err)
	}
	return nil
}

// storeDir is <path>/<key>, after checking the store root exists: a missing
// root is a misconfigured volume, never an empty store.
func storeDir(path, key string) (string, error) {
	if path == "" {
		return "", errors.New("git store path is required")
	}
	if err := validStoreKey(key); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("git store %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("git store %s is not a directory", path)
	}
	return filepath.Join(path, cacheLayout, key), nil
}

// storeKeys lists the key directories under a store root.
func storeKeys(path string) ([]string, error) {
	if _, err := storeDir(path, "probe"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(path, cacheLayout))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list git store %s: %w", path, err)
	}
	keys := []string{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			keys = append(keys, entry.Name())
		}
	}
	return keys, nil
}

// dirSize sums the sizes of every regular file under dir.
func dirSize(dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}
