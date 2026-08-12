package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/google/uuid"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

var ErrExpired = dbcontext.ErrConnectionExpired

type Options struct {
	Dir    string
	MaxAge time.Duration
	Now    func() time.Time
}

type Manager struct {
	mu       sync.RWMutex
	dir      string
	maxAge   time.Duration
	now      func() time.Time
	items    map[string]*snapshot
	profiles map[string]string
	expired  map[string]struct{}
	stop     chan struct{}
	done     chan struct{}
}

type snapshot struct {
	id           string
	connection   models.Connection
	createdAt    time.Time
	lastAccessed time.Time
	age          time.Duration
	path         string
	db           *sql.DB
	stats        query.ReconcileStats
	source       string
	dest         string
	sourceCut    bool
	destCut      bool
	profiles     map[string]materialization
	leases       int
}

type materialization struct {
	profile query.Profile
	table   string
	columns []query.ColumnDef
	rows    int
}

func New(options Options) (*Manager, error) {
	if strings.TrimSpace(options.Dir) == "" {
		return nil, fmt.Errorf("snapshot directory is required")
	}
	if options.MaxAge <= 0 {
		return nil, fmt.Errorf("snapshot maximum age must be positive")
	}
	abs, err := filepath.Abs(options.Dir)
	if err != nil {
		return nil, fmt.Errorf("resolve snapshot directory: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		dir: abs, maxAge: options.MaxAge, now: now,
		items: map[string]*snapshot{}, profiles: map[string]string{}, expired: map[string]struct{}{},
	}, nil
}

// Prepare clears files left by a previous process and creates the private root.
// Serve calls it at startup; tests and direct callers can create lazily.
func (m *Manager) Prepare() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.items) > 0 {
		return fmt.Errorf("cannot prepare snapshots while snapshots exist")
	}
	if err := os.RemoveAll(m.dir); err != nil {
		return fmt.Errorf("clear orphaned snapshots: %w", err)
	}
	if err := m.ensureRootLocked(); err != nil {
		return err
	}
	if m.stop == nil {
		m.stop, m.done = make(chan struct{}), make(chan struct{})
		go m.pruneLoop(m.stop, m.done)
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, result *query.ReconcileResult, age time.Duration) (profiles.ReconcileSnapshotDescriptor, error) {
	if result == nil {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("reconciliation result is required")
	}
	if age == 0 {
		age = m.maxAge
	}
	if age <= 0 {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("snapshot age must be positive")
	}
	if age > m.maxAge {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("snapshot age %s cannot exceed server maximum %s", age, m.maxAge)
	}

	id := uuid.NewString()
	short := strings.ReplaceAll(id, "-", "")[:12]
	m.mu.Lock()
	err := m.ensureRootLocked()
	m.mu.Unlock()
	if err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, err
	}
	dir := filepath.Join(m.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("create snapshot %q: %w", id, err)
	}
	path := filepath.Join(dir, "snapshot.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("open snapshot %q: %w", id, err)
	}
	cleanup := func() {
		_ = writer.Close()
		_ = os.RemoveAll(dir)
	}
	columns := result.SnapshotColumns()
	rows := result.SnapshotRows()
	if err := writeTable(ctx, writer, "reconcile_rows", columns, rows); err != nil {
		cleanup()
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("materialize reconciliation: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		cleanup()
		return profiles.ReconcileSnapshotDescriptor{}, fmt.Errorf("secure snapshot %q: %w", id, err)
	}

	profileName := "reconciliations/" + short + "/results"
	connectionName := "reconciliation-" + short
	connectionID := uuid.New()
	profile := snapshotProfile(profileName, "reconcile_rows", columns, connectionName, len(rows))
	now := m.now()
	item := &snapshot{
		id: id, path: path, db: writer, createdAt: now, lastAccessed: now, age: age,
		connection: models.Connection{
			ID: connectionID, Name: connectionName, Namespace: "reconciliations", Source: "reconcile",
			Type: models.ConnectionTypeSQLite, URL: readOnlyDSN(path), Virtual: true, ReadOnly: true,
			CreatedAt: now, UpdatedAt: now,
		},
		stats: result.Stats, source: result.Source, dest: result.Dest,
		sourceCut: result.SourceTruncated, destCut: result.DestTruncated,
		profiles: map[string]materialization{
			profileName: {profile: profile, table: "reconcile_rows", columns: columns, rows: len(rows)},
		},
	}
	item.connection.ExpiresAt = ptrTime(now.Add(age))
	m.mu.Lock()
	m.items[id] = item
	m.profiles[profileName] = id
	m.mu.Unlock()
	return m.descriptor(item, item.profiles[profileName]), nil
}

func (m *Manager) ensureRootLocked() error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	if err := os.Chmod(m.dir, 0o700); err != nil {
		return fmt.Errorf("secure snapshot directory: %w", err)
	}
	return nil
}

func (m *Manager) acquireSnapshot(id string) (*snapshot, func(), error) {
	if id == "" {
		return nil, nil, fmt.Errorf("snapshot id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[id]
	if item == nil {
		if _, found := m.expired[id]; found {
			return nil, nil, ErrExpired
		}
		return nil, nil, fmt.Errorf("snapshot %q not found", id)
	}
	release, err := m.acquireLocked(item)
	return item, release, err
}

func (m *Manager) acquireLocked(item *snapshot) (func(), error) {
	now := m.now()
	if item.leases == 0 && !now.Before(item.lastAccessed.Add(item.age)) {
		m.removeLocked(item)
		return nil, ErrExpired
	}
	item.leases++
	item.lastAccessed = now
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			current := m.items[item.id]
			if current == nil {
				return
			}
			current.leases--
			current.lastAccessed = m.now()
			current.connection.ExpiresAt = ptrTime(current.lastAccessed.Add(current.age))
		})
	}, nil
}

func (m *Manager) connectionLocked(reference string) *snapshot {
	for _, item := range m.items {
		if reference == item.connection.ID.String() || reference == item.connection.Name ||
			reference == "connection://"+item.connection.Name ||
			reference == "connection://"+item.connection.Namespace+"/"+item.connection.Name {
			return item
		}
	}
	return nil
}

func (m *Manager) SetMaxAge(age time.Duration) error {
	if age <= 0 {
		return fmt.Errorf("snapshot maximum age must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.items) > 0 {
		return fmt.Errorf("cannot change snapshot maximum age while snapshots exist")
	}
	m.maxAge = age
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop, m.done = nil, nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for _, item := range m.items {
		if err := item.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	m.items = map[string]*snapshot{}
	m.profiles = map[string]string{}
	m.expired = map[string]struct{}{}
	if err := os.RemoveAll(m.dir); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) pruneLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := min(m.maxAge/2, time.Minute)
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.prune()
		case <-stop:
			return
		}
	}
}

func (m *Manager) snapshot(id string, touch bool) (*snapshot, error) {
	if id == "" {
		return nil, fmt.Errorf("snapshot id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.items[id]
	if item == nil {
		if _, found := m.expired[id]; found {
			return nil, ErrExpired
		}
		return nil, fmt.Errorf("snapshot %q not found", id)
	}
	now := m.now()
	if item.leases == 0 && !now.Before(item.lastAccessed.Add(item.age)) {
		m.removeLocked(item)
		return nil, ErrExpired
	}
	if touch {
		item.lastAccessed = now
		item.connection.ExpiresAt = ptrTime(item.lastAccessed.Add(item.age))
	}
	return item, nil
}

func (m *Manager) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, item := range m.items {
		if item.leases == 0 && !now.Before(item.lastAccessed.Add(item.age)) {
			m.removeLocked(item)
		}
	}
}

func (m *Manager) removeLocked(item *snapshot) {
	m.expired[item.id] = struct{}{}
	for _, reference := range []string{
		item.connection.ID.String(), item.connection.Name,
		"connection://" + item.connection.Name,
		"connection://" + item.connection.Namespace + "/" + item.connection.Name,
	} {
		m.expired[reference] = struct{}{}
	}
	_ = item.db.Close()
	_ = os.RemoveAll(filepath.Dir(item.path))
	delete(m.items, item.id)
	for name := range item.profiles {
		m.expired[name] = struct{}{}
		delete(m.profiles, name)
	}
}

func (m *Manager) descriptor(item *snapshot, materialized materialization) profiles.ReconcileSnapshotDescriptor {
	expires := item.lastAccessed.Add(item.age)
	return profiles.ReconcileSnapshotDescriptor{
		ID: item.id, Connection: "connection://" + item.connection.Namespace + "/" + item.connection.Name,
		ConnectionID: item.connection.ID.String(), Profile: materialized.profile.Name,
		Surface: "profile-" + profileSlug(materialized.profile.Name),
		URL:     "/api/v1/profile/" + url.PathEscape(materialized.profile.Name),
		Columns: slices.Clone(materialized.columns), RowCount: materialized.rows, Stats: item.stats,
		Source: item.source, Dest: item.dest, SourceLimited: item.sourceCut, DestLimited: item.destCut,
		IdleAge: item.age, ExpiresAt: expires,
	}
}

func readOnlyDSN(path string) string { return "file:" + filepath.ToSlash(path) + "?mode=ro" }

func ptrTime(value time.Time) *time.Time { return &value }

func profileSlug(name string) string {
	var slug strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			slug.WriteRune(char)
		} else if strings.ContainsRune(" -_/.", char) {
			slug.WriteRune('-')
		}
	}
	return strings.Trim(slug.String(), "-")
}
