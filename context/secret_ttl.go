package context

import (
	gocontext "context"
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

type secretScopeKey struct{}

// secretScope holds, in memory, the values resolved through the contexts that
// carry it.
type secretScope struct {
	ttl    time.Duration
	flight singleflight.Group
	values *cache.Cache
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
	id      string
	ttl     time.Duration
	resolve func() (value string, expires time.Time, err error)
}

// get is the value the context's secret scope holds, else the process cache's,
// else the resolved one.
func (l envLookup) get(ctx Context) (string, error) {
	var value envValue
	var err error
	if scope := secretScopeOf(ctx); scope != nil {
		value, err = scope.get(l)
	} else {
		value, err = l.processValue(0)
	}
	return value.value, err
}

// processValue is the process cache's value when it is younger than maxAge (0
// for any), else the resolved one.
func (l envLookup) processValue(maxAge time.Duration) (envValue, error) {
	if cached, found := envCache.Get(l.id); found {
		if value := cached.(envValue); maxAge == 0 || time.Since(value.resolved) < maxAge {
			return value, nil
		}
	}
	resolved, err, _ := envLookupGroup.Do(l.id, func() (any, error) {
		value, expires, err := l.resolve()
		if err != nil {
			return envValue{}, err
		}
		ttl := l.ttl
		if !expires.IsZero() {
			ttl = time.Until(expires)
		}
		result := envValue{value: value, resolved: time.Now(), expires: expires}
		envCache.Set(l.id, result, ttl)
		return result, nil
	})
	if err != nil {
		return envValue{}, err
	}
	return resolved.(envValue), nil
}

func (s *secretScope) get(l envLookup) (envValue, error) {
	resolved, err, _ := s.flight.Do(l.id, func() (any, error) {
		if cached, found := s.values.Get(l.id); found {
			if value := cached.(envValue); s.keeps(value) {
				return value, nil
			}
		}
		value, err := l.processValue(s.ttl)
		if err == nil {
			s.values.Set(l.id, value, cache.NoExpiration)
		}
		return value, err
	})
	if err != nil {
		return envValue{}, err
	}
	return resolved.(envValue), nil
}

func (s *secretScope) keeps(value envValue) bool {
	if !value.expires.IsZero() && !time.Now().Before(value.expires) {
		return false
	}
	return s.ttl == 0 || time.Since(value.resolved) < s.ttl
}

// dropEnvValue makes the next lookup of id resolve it again.
func dropEnvValue(ctx Context, id string) {
	envCache.Delete(id)
	if scope := secretScopeOf(ctx); scope != nil {
		scope.values.Delete(id)
	}
}
