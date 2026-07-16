// Package migrate applies declarative Atlas HCL schemas to PostgreSQL databases.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	_ "ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"
	"github.com/flanksource/commons/logger"
	_ "github.com/lib/pq"
	"github.com/zclconf/go-cty/cty"
)

type options struct {
	dir             string
	name            string
	exclude         []string
	allowTableDrops bool
	input           map[string]cty.Value
}

// Option configures an HCL migration.
type Option func(*options)

// WithDir selects the root containing colocated HCL and SQL migrations.
func WithDir(dir string) Option {
	return func(o *options) { o.dir = strings.Trim(path.Clean(dir), "/") }
}

// WithName sets the metadata scope used for SQL hashes and managed security.
// It should be stable and unique for each migration bundle sharing a database.
func WithName(name string) Option {
	return func(o *options) { o.name = strings.TrimSpace(name) }
}

// WithVariables supplies values for HCL variable blocks and security expressions.
func WithVariables(input map[string]cty.Value) Option {
	return func(o *options) { o.input = input }
}

// WithExclude excludes database objects from Atlas inspection. Values use
// Atlas's schema inspection patterns (for example "table.column").
func WithExclude(patterns ...string) Option {
	return func(o *options) { o.exclude = append(o.exclude, patterns...) }
}

// WithTableDrops allows HCL files to remove tables. Drops are suppressed by
// default so a partial schema bundle cannot delete tables owned by consumers.
func WithTableDrops() Option {
	return func(o *options) { o.allowTableDrops = true }
}

// Apply loads colocated HCL and SQL migrations from schemaFS. SQL scripts marked
// phase pre run before the Atlas realm diff; all other SQL defaults to the post
// phase. Declared PostgreSQL roles and permissions are reconciled last. Tables
// absent from a partial schema bundle are never dropped unless WithTableDrops is
// supplied.
func Apply(ctx context.Context, connection string, schemaFS fs.FS, opts ...Option) error {
	if strings.TrimSpace(connection) == "" {
		return errors.New("connection string is empty")
	}
	if schemaFS == nil {
		return errors.New("schema filesystem is nil")
	}
	cfg := options{dir: "."}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.dir == "" {
		cfg.dir = "."
	}
	if cfg.name == "" {
		cfg.name = cfg.dir
		if cfg.name == "." {
			cfg.name = "default"
		}
	}

	scripts, err := loadScripts(schemaFS, cfg.dir)
	if err != nil {
		return err
	}
	parser, security, err := loadHCL(schemaFS, cfg.dir, cfg.input)
	if err != nil {
		return err
	}

	db, err := sql.Open("postgres", connection)
	if err != nil {
		return fmt.Errorf("open SQL migration database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect SQL migration database: %w", err)
	}
	if err := ensureMetadataTables(ctx, db); err != nil {
		return err
	}
	selected, err := selectScripts(ctx, db, cfg.name, scripts)
	if err != nil {
		return err
	}
	ordered, err := topologicalScripts(scripts, selected)
	if err != nil {
		return err
	}
	if err := runScriptPhase(ctx, db, cfg.name, ordered, phasePre); err != nil {
		return err
	}

	client, err := sqlclient.Open(ctx, connectionWithLockTimeout(connection))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer client.Close()

	current, err := atlasmigrate.SchemaConn(client.Driver, client.URL.Schema, &schema.InspectOptions{Exclude: cfg.exclude}).ReadState(ctx)
	if err != nil {
		return fmt.Errorf("inspect current schema: %w", err)
	}
	desired := &schema.Realm{}
	if err := client.Eval(parser, desired, cfg.input); err != nil {
		return fmt.Errorf("evaluate HCL schemas: %w", err)
	}

	changes, err := client.RealmDiff(current, desired)
	if err != nil {
		return fmt.Errorf("compute schema diff: %w", err)
	}
	if !cfg.allowTableDrops {
		changes = withoutTableDrops(changes)
	}
	if len(changes) == 0 {
		logger.GetLogger("migrate").Debugf("No schema changes detected")
	} else {
		invalidated, err := invalidateDependentViews(ctx, db, cfg.name, changes, scripts)
		if err != nil {
			return err
		}
		if len(invalidated) > 0 {
			if selected, err = selectScripts(ctx, db, cfg.name, scripts); err != nil {
				return err
			}
			if ordered, err = topologicalScripts(scripts, selected); err != nil {
				return err
			}
		}
		plan, err := client.PlanChanges(ctx, "", changes)
		if err != nil {
			return fmt.Errorf("plan %d schema changes: %w", len(changes), err)
		}
		log := logger.GetLogger("migrate")
		for _, change := range plan.Changes {
			log.Tracef("%s", change.Cmd)
		}
		if err := client.ApplyChanges(ctx, changes); err != nil {
			return fmt.Errorf("apply %d schema changes: %w", len(changes), err)
		}
		log.V(1).Infof("Applied %d schema changes", len(changes))
	}
	if err := runScriptPhase(ctx, db, cfg.name, ordered, phasePost); err != nil {
		return err
	}
	if err := retryOnLockContention(ctx, "reconcile database security", func() error {
		return reconcileSecurity(ctx, db, cfg.name, security)
	}); err != nil {
		return fmt.Errorf("reconcile database security: %w", err)
	}
	return nil
}

func withoutTableDrops(changes []schema.Change) []schema.Change {
	filtered := make([]schema.Change, 0, len(changes))
	for _, change := range changes {
		if drop, ok := change.(*schema.DropTable); ok {
			logger.GetLogger("migrate").Tracef("Skipping drop table of %s", drop.T.Name)
			continue
		}
		filtered = append(filtered, change)
	}
	return filtered
}

// connectionWithLockTimeout appends a libpq `options` runtime parameter so every
// connection Atlas opens bounds how long its ALTER TABLE DDL waits for a lock. A
// migration that would otherwise block indefinitely against a live reader fails
// fast (55P03) and the next apply re-plans from a fresh inspect, instead of
// camping on an ACCESS EXCLUSIVE lock and starving concurrent traffic. The value
// is returned unchanged when it is not a URL-form DSN or already sets options.
func connectionWithLockTimeout(connection string) string {
	u, err := url.Parse(connection)
	if err != nil || u.Scheme == "" {
		return connection
	}
	q := u.Query()
	if q.Get("options") != "" {
		return connection
	}
	q.Set("options", "-c lock_timeout="+migrationLockTimeout)
	u.RawQuery = q.Encode()
	return u.String()
}
