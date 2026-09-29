// Store: one process's hold on a record store — as its owner, writing the
// file and ingesting the spool, or as a reader handing its writes over the
// spool and waiting to take over.
package owner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/gofrs/flock"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
)

// Target is a backend a store can be elected over: opened read-only it
// submits its writes, and Promote makes it write the file itself.
type Target interface {
	recordstore.Backend
	recordstore.BatchAppender
	Promote(ctx context.Context) error
	ProducerSeq(ctx context.Context, instance string) (int64, error)
	SweepBatches(ctx context.Context, before time.Time, keep func(id string) bool) (int, error)
}

// Role is what a process does with a store.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleReader Role = "reader"
)

var (
	// ErrPending reports a write handed to the owner that the caller stopped
	// waiting for: it is durable in the spool and will be applied; Await
	// reports how. An unkeyed write must not be retried blindly.
	ErrPending = errors.New("record store write is pending")

	// ErrCatalogVersion reports a store whose owner reads another catalog
	// version than this build: writes still reach it through the spool, but
	// this process cannot read the file.
	ErrCatalogVersion = errors.New("record store owner runs another catalog version")
)

// PendingError is ErrPending for one batch.
type PendingError struct{ BatchID string }

func (e *PendingError) Error() string { return fmt.Sprintf("batch %s: %s", e.BatchID, ErrPending) }

func (e *PendingError) Unwrap() error { return ErrPending }

// CatalogVersionError is ErrCatalogVersion with both versions.
type CatalogVersionError struct{ Owner, Build int }

func (e *CatalogVersionError) Error() string {
	return fmt.Sprintf("%s: the owner reads catalog version %d, this build %d", ErrCatalogVersion, e.Owner, e.Build)
}

func (e *CatalogVersionError) Unwrap() error { return ErrCatalogVersion }

// Options configure Open.
type Options[T Target] struct {
	// Path is the store as configured; every file beside it derives from it.
	Path string

	// Open opens the backend: writable for the owner, or read-only handing
	// its writes to submit.
	Open func(ctx context.Context, readOnly bool, submit recordstore.Submitter) (T, error)

	// Store is the file Open opens, and CatalogVersion the catalog this
	// build reads; the owner publishes both, and a reader of another catalog
	// version cannot read the file.
	Store          string
	CatalogVersion int

	// Build names this build in the state it publishes as owner.
	Build string

	// Format is the spool format this process publishes; empty is ndjson.
	Format string

	// StartWait bounds how long a reader waits for the owner to be ready;
	// zero is a minute.
	StartWait time.Duration

	// DrainOnClose bounds how long Close ingests what is left in the spool,
	// as owner, or when the owner is gone; zero is ten seconds.
	DrainOnClose time.Duration

	// Poll is the longest the owner waits between looks at the spool when
	// nothing nudges it; zero is 500ms.
	Poll time.Duration
}

const (
	minPoll        = 50 * time.Millisecond
	awaitPoll      = 25 * time.Millisecond
	heartbeatEvery = 5 * time.Second
	collectEvery   = time.Minute
	ledgerKeep     = 7 * 24 * time.Hour
	promotePoll    = 100 * time.Millisecond
)

// Store is this process's hold on a record store.
type Store[T Target] struct {
	options    Options[T]
	configured string
	instance   string
	producer   *spool.Producer
	spool      spool.Dir
	socket     string
	shared     bool

	mu         sync.Mutex
	role       Role
	backend    T
	opened     bool
	catalogErr error
	lock       *flock.Flock
	control    *ControlServer
	state      State
	onRole     []func(Role)
	published  map[string]string

	ingest  *ingester[T]
	ctx     context.Context
	cancel  context.CancelFunc
	running sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

// Open takes a hold on the store options.Path names: as its owner when no
// other process holds it, otherwise as a reader that takes over when the
// owner goes. Within one process every Open of a path shares one Store,
// released by the last Close.
func Open[T Target](ctx context.Context, options Options[T]) (*Store[T], error) {
	return open(ctx, options, true)
}

func open[T Target](ctx context.Context, options Options[T], shared bool) (*Store[T], error) {
	if options.Open == nil {
		return nil, errors.New("record store owner: an Open function is required")
	}
	configured, err := absolute(options.Path)
	if err != nil {
		return nil, err
	}
	if shared {
		return share(configured, func() (*Store[T], error) { return start(ctx, options, configured, true) })
	}
	return start(ctx, options, configured, false)
}

func start[T Target](ctx context.Context, options Options[T], configured string, shared bool) (*Store[T], error) {
	if options.Format == "" {
		options.Format = spool.FormatNDJSON
	}
	if options.StartWait <= 0 {
		options.StartWait = time.Minute
	}
	if options.DrainOnClose <= 0 {
		options.DrainOnClose = 10 * time.Second
	}
	if options.Poll <= 0 {
		options.Poll = 500 * time.Millisecond
	}
	if err := prepare(configured); err != nil {
		return nil, err
	}
	dir, err := spool.OpenDir(spoolPath(configured))
	if err != nil {
		return nil, err
	}
	instance := newInstance()
	lifetime, cancel := context.WithCancel(context.Background())
	store := &Store[T]{
		options: options, configured: configured, instance: instance, producer: spool.NewProducer(instance, options.Build),
		spool: dir, socket: SocketPath(configured), shared: shared, published: map[string]string{},
		ctx: lifetime, cancel: cancel, role: RoleReader,
	}
	lock, held, err := tryLock(configured)
	if err != nil {
		cancel()
		return nil, err
	}
	if held {
		if err := store.own(ctx, lock); err != nil {
			cancel()
			return nil, errors.Join(err, lock.Close())
		}
		return store, nil
	}
	if err := store.read(ctx); err != nil {
		cancel()
		return nil, err
	}
	return store, nil
}

// read opens the store as a reader once its owner is ready, and starts
// waiting for the lock. An owner gone before it was ready is taken over.
func (s *Store[T]) read(ctx context.Context) error {
	state, err := s.awaitOwner(ctx)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	if state.CatalogVersion != s.options.CatalogVersion {
		s.catalogErr = &CatalogVersionError{Owner: state.CatalogVersion, Build: s.options.CatalogVersion}
	} else {
		backend, err := s.options.Open(ctx, true, s.submit)
		if err != nil {
			return fmt.Errorf("record store owner: open %s read-only: %w", s.configured, err)
		}
		s.backend, s.opened = backend, true
	}
	s.running.Go(s.promoter)
	return nil
}

// awaitOwner waits until the owner's state says it is ready, and returns it.
// If the lock comes free while waiting, this process takes the store and
// returns no state.
func (s *Store[T]) awaitOwner(ctx context.Context) (*State, error) {
	deadline := time.Now().Add(s.options.StartWait)
	for {
		state, found, err := ReadState(s.configured)
		if err != nil {
			return nil, err
		}
		if found && state.Phase == PhaseReady {
			return &state, nil
		}
		lock, held, err := tryLock(s.configured)
		if err != nil {
			return nil, err
		}
		if held {
			if err := s.own(ctx, lock); err != nil {
				return nil, errors.Join(err, lock.Close())
			}
			return nil, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("record store owner: the owner of %s was not ready within %s", s.configured, s.options.StartWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(minPoll):
		}
	}
}

// promoter waits for the lock and takes the store over once it has it.
func (s *Store[T]) promoter() {
	lock := flock.New(lockPath(s.configured))
	held, err := lock.TryLockContext(s.ctx, promotePoll)
	if err != nil || !held {
		if s.ctx.Err() == nil {
			logger.Errorf("record store owner %s: wait for the lock: %v", s.configured, err)
		}
		_ = lock.Close()
		return
	}
	if err := s.own(s.ctx, lock); err != nil {
		logger.Errorf("record store owner %s: take over: %v", s.configured, err)
		_ = lock.Close()
	}
}

// own makes this process the store's owner under lock: it publishes its
// state, opens or promotes the backend, serves the control socket and starts
// ingesting the spool.
func (s *Store[T]) own(ctx context.Context, lock *flock.Flock) error {
	state := processState(s.instance, s.options.Build, PhaseStarting)
	state.Store, state.CatalogVersion, state.Socket = s.options.Store, s.options.CatalogVersion, s.socket
	state.Spool = &SpoolState{Dir: s.spool.Path(), ManifestFormat: spool.ManifestFormat, Formats: spool.Formats()}
	if err := writeState(s.configured, state); err != nil {
		return err
	}
	failpoint("during-starting")
	s.mu.Lock()
	backend, opened := s.backend, s.opened
	s.mu.Unlock()
	if opened {
		if err := backend.Promote(ctx); err != nil {
			return errors.Join(err, removeState(s.configured, s.instance))
		}
	} else {
		var err error
		if backend, err = s.options.Open(ctx, false, nil); err != nil {
			return errors.Join(fmt.Errorf("record store owner: open %s: %w", s.configured, err), removeState(s.configured, s.instance))
		}
	}
	s.mu.Lock()
	s.backend, s.opened, s.catalogErr, s.lock, s.role = backend, true, nil, lock, RoleOwner
	s.ingest = newIngester(s.spool, backend, s.configured)
	s.mu.Unlock()
	control, err := ServeControl(s.socket, ControlHandler{Status: s.ownerState, Ingest: s.ingest.request})
	if err != nil {
		logger.Warnf("record store owner %s: serving no control socket, producers wait for the poll: %v", s.configured, err)
	}
	state.Phase = PhaseReady
	s.mu.Lock()
	s.control, s.state = control, state
	callbacks := append([]func(Role){}, s.onRole...)
	s.mu.Unlock()
	if err := writeState(s.configured, state); err != nil {
		return err
	}
	s.running.Go(func() { s.ingest.loop(s.ctx, s.options.Poll) })
	s.running.Go(s.heartbeat)
	for _, callback := range callbacks {
		callback(RoleOwner)
	}
	return nil
}

// ownerState is the state this owner publishes, with the spool's figures.
func (s *Store[T]) ownerState() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	if s.ingest != nil {
		state.Backlog, state.Failed, state.LastError = s.ingest.figures()
	}
	return state
}

// heartbeat republishes the owner's state and collects the spool and the
// ledger, until the store closes.
func (s *Store[T]) heartbeat() {
	beat := time.NewTicker(heartbeatEvery)
	defer beat.Stop()
	collect := time.NewTicker(collectEvery)
	defer collect.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-beat.C:
			state := s.ownerState()
			state.HeartbeatAt = time.Now().UTC()
			s.mu.Lock()
			s.state.HeartbeatAt = state.HeartbeatAt
			s.mu.Unlock()
			if err := writeState(s.configured, state); err != nil {
				logger.Errorf("record store owner %s: heartbeat: %v", s.configured, err)
			}
		case <-collect.C:
			if err := s.ingest.collect(s.ctx, time.Now()); err != nil {
				logger.Errorf("record store owner %s: collect: %v", s.configured, err)
			}
		}
	}
}

// Backend is the store's backend: writable for the owner, read-only for a
// reader. A reader of an owner on another catalog version has none, and gets
// a *CatalogVersionError.
func (s *Store[T]) Backend() (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalogErr != nil {
		var none T
		return none, s.catalogErr
	}
	return s.backend, nil
}

// Role is what this process does with the store now.
func (s *Store[T]) Role() Role {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.role
}

// OnRole calls fn whenever this process's role changes: when a reader takes
// the store over, fn is called with RoleOwner.
func (s *Store[T]) OnRole(fn func(Role)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRole = append(s.onRole, fn)
}

// Status is the owner's state: this process's own as owner, or the one the
// owner published.
func (s *Store[T]) Status() (State, error) {
	if s.Role() == RoleOwner {
		return s.ownerState(), nil
	}
	state, found, err := ReadState(s.configured)
	if err == nil && !found {
		err = fmt.Errorf("record store owner: %s has no owner", s.configured)
	}
	return state, err
}
