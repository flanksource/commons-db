package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"gorm.io/gorm"
)

// runtime holds the request-time dependencies that entity closures resolve
// lazily. Entities are registered during the shadow init (main.go) — before the
// embedded database exists — so the DB and execution context are injected by
// runServe once they are ready. CLI commands that need neither (profile reads,
// base profile execution) work without serve; connection commands fail loudly
// until a DB is set.
var runtime = struct {
	mu    sync.RWMutex
	db    *gorm.DB
	ctx   dbcontext.Context
	store *ProfileStore
}{ctx: dbcontext.NewContext(context.Background())}

// setStore records the shared profile store created during the shadow init so
// runServe (execHandler, schemaHandler) reuses the same instance the entities
// were registered against.
func setStore(s *ProfileStore) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.store = s
}

// currentStore returns the shared profile store.
func currentStore() *ProfileStore {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.store
}

// setDB injects the database handle for connection entity operations.
func setDB(db *gorm.DB) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.db = db
}

// currentDB returns the injected database, erroring when none is set (the base
// CLI has no database — connection CRUD requires `query serve`).
func currentDB() (*gorm.DB, error) {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.db == nil {
		return nil, fmt.Errorf("connections require a database; run `query serve`")
	}
	return runtime.db, nil
}

// setContext injects the DB-backed execution context used by profile execution.
func setContext(ctx dbcontext.Context) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.ctx = ctx
}

// currentContext returns the execution context, defaulting to a DB-less context
// sufficient for the base profile flow (only postgres/sqlite processors need a DB).
func currentContext() dbcontext.Context {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.ctx
}

// nestedBody returns the request's full nested JSON body when invoked over HTTP —
// clicky stashes the wrapped *http.Request in the context (RequestFromContext) so
// nested fields (connection properties, profile provider/params/columns) survive
// the executor's flag-flattening. On the CLI path there is no request, so the
// flattened body clicky passed through is used.
func nestedBody(ctx context.Context, fallback map[string]any) (map[string]any, error) {
	r, ok := rpc.RequestFromContext(ctx)
	if !ok || r.Body == nil {
		return fallback, nil
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	return body, nil
}
