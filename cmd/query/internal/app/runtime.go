package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"gorm.io/gorm"
)

type Runtime struct {
	mu      sync.RWMutex
	db      *gorm.DB
	context dbcontext.Context
	store   profiles.Store
}

func NewRuntime(ctx dbcontext.Context, store profiles.Store) (*Runtime, error) {
	if ctx.Context.Context == nil {
		return nil, fmt.Errorf("query context is required")
	}
	if store == nil {
		return nil, fmt.Errorf("profile store is required")
	}
	return &Runtime{context: ctx, store: store}, nil
}

func (r *Runtime) SetDatabase(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("query database is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
	return nil
}

func (r *Runtime) Database() (*gorm.DB, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.db == nil {
		return nil, fmt.Errorf("connections require a database; run `query serve`")
	}
	return r.db, nil
}

func (r *Runtime) SetContext(ctx dbcontext.Context) error {
	if ctx.Context.Context == nil {
		return fmt.Errorf("query context is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.context = ctx
	return nil
}

func (r *Runtime) Context() dbcontext.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.context
}

func (r *Runtime) SetProfileStore(store profiles.Store) error {
	if store == nil {
		return fmt.Errorf("profile store is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	return nil
}

func (r *Runtime) ProfileStore() (profiles.Store, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store == nil {
		return nil, fmt.Errorf("profile store is not initialized")
	}
	return r.store, nil
}

func DecodeBody(ctx context.Context, fallback map[string]any) (map[string]any, error) {
	request, ok := rpc.RequestFromContext(ctx)
	if !ok || request.Body == nil {
		return fallback, nil
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	return body, nil
}
