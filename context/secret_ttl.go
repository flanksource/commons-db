package context

import (
	gocontext "context"
	"fmt"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

type secretScopeKey struct{}

// secretScope holds, in memory, the values resolved through the contexts that
// carry it.
type secretScope struct {
	ttl    time.Duration
	values *cache.Cache
}

// The lock makes flight registration, cache writes and invalidation atomic.
// Only current in-flight resolutions are tracked.
var (
	envCacheMu sync.Mutex
	envPending = make(map[string]*envResolution)
)

type envResolution struct {
	flight singleflight.Group
	scopes map[*secretScope]struct{}
}

// WithSecretTTL keeps every value resolved through this context and those
// derived from it (secrets, configmaps, helm values, service account tokens,
// 1Password references) for ttl, in place of the envvar.*.cache.timeout
// properties; 0 keeps them for as long as the context is in use. A value that
// expires by itself is never kept past its expiry, and failures are not kept.
// The values are this context's own: another call starts afresh, except on a
// context already at ttl, which is returned unchanged.
func (k Context) WithSecretTTL(ttl time.Duration) Context {
	if ttl < 0 {
		panic(fmt.Sprintf("secret TTL is negative: %s", ttl))
	}
	if scope := secretScopeOf(k); scope != nil && scope.ttl == ttl {
		return k
	}
	return k.WithValue(secretScopeKey{}, &secretScope{ttl: ttl, values: cache.New(cache.NoExpiration, 0)})
}

func secretScopeOf(ctx gocontext.Context) *secretScope {
	scope, _ := ctx.Value(secretScopeKey{}).(*secretScope)
	return scope
}

// envValue is a resolved value; expires is zero for one that does not expire
// by itself.
type envValue struct {
	value    string
	resolved time.Time
	expires  time.Time
}

// envLookup resolves one value, cached under id. ttl is how long the process
// cache keeps a value that does not expire by itself.
type envLookup struct {
	id            string
	ttl           time.Duration
	ambientPrompt bool
	resolve       func(Context) (value string, expires time.Time, err error)
}

// get is the value the context's secret scope holds, else the process cache's,
// else the resolved one.
func (l envLookup) get(ctx Context) (string, error) {
	scope := secretScopeOf(ctx)
	if scope != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	envCacheMu.Lock()
	if value, found := l.cachedValue(scope); found {
		envCacheMu.Unlock()
		return value.value, nil
	}
	pending := envPending[l.id]
	if pending == nil {
		pending = &envResolution{scopes: make(map[*secretScope]struct{})}
		envPending[l.id] = pending
	}
	if scope != nil {
		pending.scopes[scope] = struct{}{}
	}
	resolved := pending.flight.DoChan(l.id, func() (any, error) {
		return l.resolveValue(ctx, pending, scope != nil)
	})
	envCacheMu.Unlock()

	var done <-chan struct{}
	if scope != nil {
		done = ctx.Done()
	}
	select {
	case <-done:
		return "", ctx.Err()
	case result := <-resolved:
		if scope != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		if result.Err != nil {
			return "", result.Err
		}
		value := result.Val.(envValue)
		if !value.usable(time.Now(), 0) {
			return "", fmt.Errorf("resolved value expired while waiting for lookup")
		}
		return value.value, nil
	}
}

// cachedValue is called with envCacheMu held.
func (l envLookup) cachedValue(scope *secretScope) (envValue, bool) {
	var maxAge time.Duration
	if scope != nil {
		maxAge = scope.ttl
		if cached, found := scope.values.Get(l.id); found {
			if value := cached.(envValue); value.usable(time.Now(), maxAge) {
				return value, true
			}
		}
	}
	if cached, found := envCache.Get(l.id); found {
		if value := cached.(envValue); value.usable(time.Now(), maxAge) {
			if scope != nil {
				scope.values.Set(l.id, value, cache.NoExpiration)
			}
			return value, true
		}
	}
	return envValue{}, false
}

func (l envLookup) resolveValue(ctx Context, pending *envResolution, scoped bool) (envValue, error) {
	if scoped {
		// The shared resolver has its own timeout, independent of its waiters.
		ctx.Context.Context = gocontext.WithoutCancel(ctx.Context.Context)
		var cancel gocontext.CancelFunc
		ctx, cancel = ctx.WithTimeout(envLookupTimeout(ctx, l.ambientPrompt))
		defer cancel()
	}
	value, expires, err := l.resolve(ctx)

	envCacheMu.Lock()
	defer envCacheMu.Unlock()
	current := envPending[l.id] == pending
	if current {
		delete(envPending, l.id)
	}
	if err != nil {
		return envValue{}, err
	}

	result := envValue{value: value, resolved: time.Now(), expires: expires}
	ttl := l.ttl
	if !expires.IsZero() {
		ttl = expires.Sub(result.resolved)
		if ttl <= 0 {
			return envValue{}, fmt.Errorf("resolved value expired before caching")
		}
	}
	if current {
		envCache.Set(l.id, result, ttl)
		for scope := range pending.scopes {
			scope.values.Set(l.id, result, cache.NoExpiration)
		}
	}
	return result, nil
}

func (v envValue) usable(now time.Time, maxAge time.Duration) bool {
	return (v.expires.IsZero() || v.expires.After(now)) &&
		(maxAge == 0 || now.Sub(v.resolved) < maxAge)
}

// dropEnvValue makes the next lookup of id resolve it again.
func dropEnvValue(ctx Context, id string) {
	envCacheMu.Lock()
	defer envCacheMu.Unlock()
	delete(envPending, id)
	envCache.Delete(id)
	if scope := secretScopeOf(ctx); scope != nil {
		scope.values.Delete(id)
	}
}
