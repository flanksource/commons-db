package git

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons-db/git/readcache"
)

var lookupCaches sync.Map

type readCacheKey struct{}
type securityNamespaceKey struct{}
type freshRefsKey struct{}

func WithFreshRefs(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshRefsKey{}, true)
}
func FreshRefs(ctx context.Context) bool { fresh, _ := ctx.Value(freshRefsKey{}).(bool); return fresh }

func lookupKey(ctx context.Context, repo *Repository, operation string, options any) (string, error) {
	namespace, _ := ctx.Value(securityNamespaceKey{}).(string)
	provider := "git"
	if repo.Provider != nil {
		provider = repo.Provider.Type()
	}
	identity, err := json.Marshal(struct {
		Version                        int
		URL                            RepoURL
		Auth                           Auth
		Namespace, Provider, Operation string
		Options                        any
	}{1, repo.URL, repo.Auth, repo.SecurityNamespace + "\x00" + namespace, provider, operation, options})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("git-read-v1:%x", sha256.Sum256(identity)), nil
}

func (r *Repository) InvalidateRefs(ctx context.Context) error {
	cache, ok := ctx.Value(readCacheKey{}).(*readcache.Cache)
	if !ok {
		cache = r.ReadCache
	}
	if cache == nil {
		return nil
	}
	key, err := lookupKey(ctx, r, "refs", nil)
	if err != nil {
		return err
	}
	return cache.Invalidate(ctx, key)
}

func (r *Repository) CacheKey() string {
	identity, err := json.Marshal(struct {
		URL       RepoURL
		Auth      Auth
		Namespace string
	}{r.URL, r.Auth, r.SecurityNamespace})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(identity))
}

// WithSecurityNamespace isolates reads for callers sharing transport credentials.
func WithSecurityNamespace(ctx context.Context, namespace string) context.Context {
	if namespace == "" {
		panic("git: security namespace is empty")
	}
	return context.WithValue(ctx, securityNamespaceKey{}, namespace)
}

// WithReadCache supplies a shared adapter without retaining the request context.
func WithReadCache(ctx context.Context, cache *readcache.Cache) context.Context {
	if cache == nil || cache.Store == nil {
		panic("git: read cache requires a store")
	}
	return context.WithValue(ctx, readCacheKey{}, cache)
}

func cachedRead[T any](ctx context.Context, root string, repo *Repository, operation string, options any, load func(context.Context) (T, error)) (T, error) {
	return cachedLookup(ctx, root, repo, operation, options, 24*time.Hour, load)
}

func cachedLookup[T any](ctx context.Context, root string, repo *Repository, operation string, options any, retention time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if repo == nil {
		return zero, fmt.Errorf("git %s requires repository", operation)
	}
	cache, ok := ctx.Value(readCacheKey{}).(*readcache.Cache)
	if !ok && repo.ReadCache != nil {
		cache, ok = repo.ReadCache, true
	}
	if !ok && root == "" {
		return load(ctx)
	}
	key, err := lookupKey(ctx, repo, operation, options)
	if err != nil {
		return zero, err
	}
	if !ok {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return zero, err
		}
		value, _ := lookupCaches.LoadOrStore(absolute, &readcache.Cache{Store: readcache.Filesystem{Root: filepath.Join(absolute, "reads")}})
		cache = value.(*readcache.Cache)
	}
	metadata := readcache.Metadata{Operation: operation, Repository: repo.URL.Redacted()}
	metadata.Ref, _ = options.(string)
	ctx = readcache.WithMetadata(ctx, metadata)
	raw, err := cache.Load(ctx, key, retention, func(ctx context.Context) ([]byte, error) {
		value, err := load(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	})
	if err != nil {
		return zero, err
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, fmt.Errorf("decode cached git %s: %w", operation, err)
	}
	return zero, nil
}
