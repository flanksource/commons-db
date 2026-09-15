package recordresults

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/ndjson"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

const (
	// recordsFile is a local durable sqlite store, which is also its own index;
	// indexFile a sqlite index derived from another backend, which may be
	// dropped and rebuilt from it.
	recordsFile = "records.sqlite"
	indexFile   = "index.sqlite"
	ndjsonDir   = "ndjson"

	// sweepInterval is how often a sqlite file removes expired streams.
	sweepInterval = 10 * time.Minute
)

// OpenOptions configure Open.
type OpenOptions struct {
	// Prefix and ConnectionName name the result profiles and the index
	// connection, as RegistryOptions does.
	Prefix         string
	ConnectionName string

	// Settings say where the files live and how long a stream is kept. With no
	// Source, Settings.Backend is the local store to open, sqlite or ndjson —
	// resolve it first (Settings.Resolve).
	Settings recordstore.Settings

	// Source is a backend the caller routes itself, typically a
	// recordstore.Router over a kv store per tenant, or nil to open
	// Settings.Backend locally. A Source is always mirrored into a derived
	// index of its own, never into a file a route could also write, so one
	// route's streams cannot be read through another. Open owns Source: Close,
	// or a failed Open, closes it.
	Source recordstore.Backend

	// Schemas is the kind catalog the index and Register share. Pass one when
	// Source opens stores of its own that need it — a route's local sqlite
	// file resolves kinds the registry declares; nil makes a fresh one.
	Schemas *recordstore.Schemas

	// Register declares the result types the registry serves.
	Register func(*Registry) error
}

// Results is an open result store: the backend captures append to, and the
// registry that serves what they appended through the profile engine.
type Results struct {
	// Backend is what a capture appends its rows to. Appending through it wakes
	// every session following the stream.
	Backend *recordstore.Notifier

	// Registry serves the result types over the index.
	Registry *Registry

	closers []io.Closer
}

// Open opens the store options describe:
//
//	Source   Settings.Backend  backend        index
//	nil      sqlite            records.sqlite the same file
//	nil      ndjson            ndjson/        index.sqlite, derived
//	a Router (any)             the Router     index.sqlite, derived
func Open(options OpenOptions) (*Results, error) {
	results := &Results{}
	if options.Source != nil {
		results.closers = append(results.closers, options.Source)
	}
	if err := results.open(options); err != nil {
		return nil, errors.Join(err, results.Close())
	}
	return results, nil
}

func (o OpenOptions) validate() error {
	settings := o.Settings
	switch {
	case strings.TrimSpace(settings.Dir) == "":
		return errors.New("result store: a directory is required")
	case settings.TTL <= 0:
		return fmt.Errorf("result store: a positive stream ttl is required, got %s", settings.TTL)
	case o.Register == nil:
		return errors.New("result store: Register must declare the result types it serves")
	case o.Source != nil:
		return nil
	case settings.Backend == recordstore.BackendKV:
		return errors.New("result store: a kv backend is routed per caller; pass it as Source, a recordstore.Router")
	case settings.Backend == "":
		return errors.New("result store: no backend; Resolve the settings to sqlite or ndjson, or pass a Source")
	case settings.Backend != recordstore.BackendSQLite && settings.Backend != recordstore.BackendNDJSON:
		return fmt.Errorf("result store: backend %q is not one Open can open", settings.Backend)
	}
	return nil
}

func (r *Results) open(options OpenOptions) error {
	if err := options.validate(); err != nil {
		return err
	}
	// SQLite creates a file but not the directory it goes in.
	if err := os.MkdirAll(options.Settings.Dir, 0o755); err != nil {
		return fmt.Errorf("result store: create %s: %w", options.Settings.Dir, err)
	}
	schemas := options.Schemas
	if schemas == nil {
		schemas = recordstore.NewSchemas()
	}
	source, index, err := r.openFiles(options, schemas)
	if err != nil {
		return err
	}
	notifier, err := recordstore.NewNotifier(source, recordstore.NotifierOptions{RecheckInterval: FollowRecheckInterval})
	if err != nil {
		return fmt.Errorf("result store: %w", err)
	}
	registry, err := NewRegistry(RegistryOptions{
		Prefix: options.Prefix, Schemas: schemas, Index: index, Source: notifier, ConnectionName: options.ConnectionName,
	})
	if err != nil {
		return err
	}
	r.closers = append(r.closers, registry)
	if err := options.Register(registry); err != nil {
		return fmt.Errorf("result store: register result types: %w", err)
	}
	r.Backend, r.Registry = notifier, registry
	return nil
}

// openFiles opens the source and the index the options call for.
func (r *Results) openFiles(options OpenOptions, schemas *recordstore.Schemas) (recordstore.Backend, *sqlite.Backend, error) {
	settings := options.Settings
	if options.Source == nil && settings.Backend == recordstore.BackendSQLite {
		file, err := r.openSQLite(settings, recordsFile, schemas, false)
		return file, file, err
	}
	source := options.Source
	if source == nil {
		local, err := ndjson.New(ndjson.Options{
			Dir: filepath.Join(settings.Dir, ndjsonDir), Schema: schemas.Kind, MaxBytes: settings.NDJSONMaxBytes,
			KeepStreams: settings.NDJSONKeepStreams, TTL: settings.TTL,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("result store: open ndjson streams: %w", err)
		}
		r.closers = append(r.closers, local)
		source = local
	}
	index, err := r.openSQLite(settings, indexFile, schemas, true)
	return source, index, err
}

// openSQLite opens name under the store directory. A derived index keeps no
// expiry of its own: the Indexer gives each stream its source's.
func (r *Results) openSQLite(settings recordstore.Settings, name string, schemas *recordstore.Schemas, derived bool) (*sqlite.Backend, error) {
	ttl := settings.TTL
	if derived {
		ttl = 0
	}
	file, err := sqlite.Open(sqlite.Options{
		Path: filepath.Join(settings.Dir, name), Schema: schemas.Kind, TTL: ttl, Derived: derived,
		SweepInterval: sweepInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("result store: open %s: %w", name, err)
	}
	r.closers = append(r.closers, file)
	return file, nil
}

// Close closes everything Open opened, and the Source it was given, in
// reverse order.
func (r *Results) Close() error {
	var errs []error
	for index := len(r.closers) - 1; index >= 0; index-- {
		errs = append(errs, r.closers[index].Close())
	}
	r.closers = nil
	return errors.Join(errs...)
}
