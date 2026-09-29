// A sqlite store shared with every other process that opens its path: one of
// them owns and writes it, the rest read it and hand their writes over.
package recordresults

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/owner"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// SharedSQLite is a sqlite store held through its elected owner. It is the
// store's backend — writable while this process owns the store, read-only
// otherwise, and promoted in place when this process takes over — and
// closing it releases this process's hold.
type SharedSQLite struct {
	*sqlite.Backend
	store *owner.Store[*sqlite.Backend]
}

var _ recordstore.Backend = (*SharedSQLite)(nil)

// OpenSharedSQLite opens the sqlite store options.Path configures, shared
// with the other processes that open it; build names this build in the state
// an owner publishes. options.ReadOnly and options.Submit are the owner's to
// set. A route's Open function uses it for a route kept in a local file.
func OpenSharedSQLite(ctx context.Context, options sqlite.Options, build string) (*SharedSQLite, error) {
	store, err := owner.Open(ctx, owner.Options[*sqlite.Backend]{
		Path: options.Path, Build: build, Store: sqlite.VersionedPath(options.Path), CatalogVersion: sqlite.CatalogVersion,
		Open: func(_ context.Context, readOnly bool, submit recordstore.Submitter) (*sqlite.Backend, error) {
			opened := options
			opened.ReadOnly, opened.Submit = readOnly, submit
			return sqlite.Open(opened)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite record store %s: %w", options.Path, err)
	}
	backend, err := store.Backend()
	if err != nil {
		return nil, fmt.Errorf("sqlite record store %s: %w", options.Path, err)
	}
	return &SharedSQLite{Backend: backend, store: store}, nil
}

// Role is what this process does with the store now.
func (s *SharedSQLite) Role() owner.Role { return s.store.Role() }

// Store is this process's hold on the store, for its bulk Writer and status.
func (s *SharedSQLite) Store() *owner.Store[*sqlite.Backend] { return s.store }

// Close releases this process's hold, closing the backend.
func (s *SharedSQLite) Close() error { return s.store.Close() }
