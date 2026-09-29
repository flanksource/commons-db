// Package probe coordinates cursor-based external sources with durable record
// streams. Source adapters own transport and decoding; this package owns
// exclusive writers, cursor fences, append ordering and finalization.
package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

const DefaultMaxPages = 64

var ErrAlreadyManaged = errors.New("probe is already managed")

// Cursor is the next position to request from one source generation.
type Cursor struct {
	Generation string `json:"generation,omitempty"`
	Next       int64  `json:"next"`
}

// Batch is one source page. Next is the cursor after the page, Write is the
// source's current exclusive write cursor, and More requests another page in
// the same sample.
type Batch struct {
	Generation string
	Next       int64
	Write      int64
	More       bool
	Active     bool
	Rows       []recordstore.Row
	Summary    any
}

// Final holds rows materialized only after the source is frozen, such as calls
// that were still open when a trace ended.
type Final struct {
	Rows    []recordstore.Row
	Summary any
	Warning string
	Result  any
}

// Source reads and closes one external cursor source. Freeze must stop new
// source writes; Release gives up its remote ownership claim.
type Source interface {
	Read(context.Context, Cursor) (Batch, error)
	Freeze(context.Context) error
	Finalize(context.Context) (Final, error)
	Release(context.Context) error
}

// Committer advances source-local decoding state after a batch is durable.
// A retried Commit must be safe after the record store already accepted the
// batch, because a process can fail between those two operations.
type Committer interface {
	Commit(context.Context, Batch) error
}

// FinalCommitter publishes source-local finalization state after its rows are
// durable and before the stream is sealed.
type FinalCommitter interface {
	CommitFinal(context.Context, Final) error
}

// Renewer extends a source ownership lease.
type Renewer interface {
	Renew(context.Context) error
}

// Detacher releases local resources without freezing, finalizing, sealing or
// releasing the external source.
type Detacher interface {
	Detach(context.Context) error
}

type OpenFunc func(context.Context) (Source, error)

type DescribeFunc func(ctx context.Context, stream string) (*query.EventsRef, error)

// Writer is the record-store surface a probe needs. recordstore.Backend and
// the record result stores recordresults opens both satisfy it.
type Writer interface {
	Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error)
	Seal(ctx context.Context, stream string) error
}

// Options configure one exclusively managed probe generation.
type Options struct {
	Identity string
	Stream   string
	Kind     string
	Backend  Writer
	Describe DescribeFunc
	Cursor   Cursor
	MaxPages int

	// RenewEvery enables background lease renewal. The opened Source must
	// implement Renewer when it is positive.
	RenewEvery time.Duration
}

func (o *Options) validate() error {
	switch {
	case o.Identity == "":
		return errors.New("probe identity is required")
	case o.Backend == nil:
		return fmt.Errorf("probe %q: record store backend is required", o.Identity)
	case o.Describe == nil:
		return fmt.Errorf("probe %q: stream describer is required", o.Identity)
	case o.MaxPages < 0:
		return fmt.Errorf("probe %q: max pages %d must not be negative", o.Identity, o.MaxPages)
	case o.RenewEvery < 0:
		return fmt.Errorf("probe %q: renew interval %s must not be negative", o.Identity, o.RenewEvery)
	case o.Cursor.Next < 0:
		return fmt.Errorf("probe %q: cursor %d must not be negative", o.Identity, o.Cursor.Next)
	}
	if err := recordstore.ValidateAppend(o.Stream, o.Kind); err != nil {
		return fmt.Errorf("probe %q: %w", o.Identity, err)
	}
	if o.MaxPages == 0 {
		o.MaxPages = DefaultMaxPages
	}
	return nil
}

// ManagerOptions configure NewManager.
type ManagerOptions struct {
	// LockDir, when set, is where an armed identity is held in a lock file
	// for as long as its run lasts, so a manager in another process sharing
	// the directory refuses the identity too. Without it a manager admits one
	// writer per identity within its own process only.
	LockDir string
}

// Manager admits at most one writer for an external source identity: one in
// its process, and one across the processes sharing its LockDir.
type Manager struct {
	lockDir string

	mu    sync.Mutex
	runs  map[string]*Run
	locks map[string]*flock.Flock
}

func NewManager(options ManagerOptions) *Manager {
	return &Manager{lockDir: options.LockDir, runs: map[string]*Run{}, locks: map[string]*flock.Flock{}}
}

// Arm reserves the identity before opening the source, creates its empty
// stream, and returns only after its first durable reference is available.
func (m *Manager) Arm(ctx context.Context, options Options, open OpenFunc) (_ *Run, resultErr error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	if open == nil {
		return nil, fmt.Errorf("probe %q: source opener is required", options.Identity)
	}
	if err := m.reserve(options.Identity); err != nil {
		return nil, err
	}
	reserved := true
	defer func() {
		if reserved {
			m.forget(options.Identity, nil)
		}
	}()

	source, err := open(ctx)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("probe %q: source opener returned nil", options.Identity)
	}
	opened := true
	defer func() {
		if opened {
			resultErr = errors.Join(resultErr, source.Release(context.WithoutCancel(ctx)))
		}
	}()
	if options.RenewEvery > 0 {
		if _, ok := source.(Renewer); !ok {
			return nil, fmt.Errorf("probe %q: renew interval requires a renewable source", options.Identity)
		}
	}
	if _, err := options.Backend.Append(ctx, options.Stream, options.Kind, nil); err != nil {
		return nil, err
	}
	ref, err := options.Describe(ctx, options.Stream)
	if err != nil {
		return nil, fmt.Errorf("probe %q: describe stream %q: %w", options.Identity, options.Stream, err)
	}
	if ref == nil {
		return nil, fmt.Errorf("probe %q: describe stream %q returned no reference", options.Identity, options.Stream)
	}

	run := newRun(m, options, source, ref, context.WithoutCancel(ctx))
	m.install(options.Identity, run)
	reserved, opened = false, false
	return run, nil
}

func (m *Manager) reserve(identity string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, found := m.runs[identity]; found {
		return fmt.Errorf("probe %q: %w", identity, ErrAlreadyManaged)
	}
	if m.lockDir != "" {
		lock, err := m.lockIdentity(identity)
		if err != nil {
			return err
		}
		m.locks[identity] = lock
	}
	m.runs[identity] = nil
	return nil
}

// lockIdentity takes identity's lock file in the lock dir, named by a digest
// of the identity so any identity makes a safe file name.
func (m *Manager) lockIdentity(identity string) (*flock.Flock, error) {
	if err := os.MkdirAll(m.lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("probe %q: create lock dir %s: %w", identity, m.lockDir, err)
	}
	sum := sha256.Sum256([]byte(identity))
	lock := flock.New(filepath.Join(m.lockDir, hex.EncodeToString(sum[:])[:32]+".lock"))
	held, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("probe %q: lock: %w", identity, err)
	}
	if !held {
		return nil, fmt.Errorf("probe %q is managed by another process: %w", identity, ErrAlreadyManaged)
	}
	return lock, nil
}

func (m *Manager) install(identity string, run *Run) {
	m.mu.Lock()
	m.runs[identity] = run
	m.mu.Unlock()
}

func (m *Manager) forget(identity string, run *Run) {
	m.mu.Lock()
	if current, found := m.runs[identity]; found && (current == run || current == nil) {
		delete(m.runs, identity)
		if lock, locked := m.locks[identity]; locked {
			_ = lock.Close()
			delete(m.locks, identity)
		}
	}
	m.mu.Unlock()
}

// Get returns the locally managed run for identity. A source still arming is
// reported as absent until Arm returns it.
func (m *Manager) Get(identity string) (*Run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[identity]
	return run, run != nil
}
