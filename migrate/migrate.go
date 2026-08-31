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
	dir        string
	name       string
	schema     string
	exclude    []string
	allowDrops bool
	input      map[string]cty.Value
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

// WithSchema selects the PostgreSQL schema used for the complete migration lifecycle.
func WithSchema(name string) Option {
	return func(o *options) { o.schema = name }
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

// WithDrops allows HCL files to remove tables and types. Drops are suppressed by
// default so a partial schema bundle cannot delete objects owned by consumers.
//
// Tables and types share one switch because suppressing only tables is worse
// than suppressing neither: the surviving table keeps a column typed by the enum
// the diff still wants to drop, and PostgreSQL rejects the DROP TYPE.
func WithDrops() Option {
	return func(o *options) { o.allowDrops = true }
}

// Apply loads colocated HCL and SQL migrations from schemaFS. SQL scripts marked
// phase pre run before the Atlas realm diff; all other SQL defaults to the post
// phase. Declared PostgreSQL roles and permissions are reconciled last. Tables
// and types absent from a partial schema bundle are never dropped unless
// WithDrops is supplied.
func Apply(ctx context.Context, connection string, schemaFS fs.FS, opts ...Option) error {
	return apply(ctx, connection, schemaFS, resolveOptions(opts))
}

func resolveOptions(opts []Option) options {
	cfg := options{dir: ".", schema: defaultSchema}
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
	cfg.exclude = append([]string(nil), cfg.exclude...)
	cfg.input = cloneVariables(cfg.input)
	return cfg
}

func cloneVariables(input map[string]cty.Value) map[string]cty.Value {
	if input == nil {
		return nil
	}
	cloned := make(map[string]cty.Value, len(input))
	for name, value := range input {
		cloned[name] = value
	}
	return cloned
}

func apply(ctx context.Context, connection string, schemaFS fs.FS, cfg options) error {
	if strings.TrimSpace(connection) == "" {
		return errors.New("connection string is empty")
	}
	if schemaFS == nil {
		return errors.New("schema filesystem is nil")
	}
	if err := ValidateSchemaName(cfg.schema); err != nil {
		return fmt.Errorf("migration schema: %w", err)
	}

	scripts, err := loadScripts(schemaFS, cfg.dir)
	if err != nil {
		return err
	}
	parser, security, err := loadHCL(schemaFS, cfg.dir, cfg.input)
	if err != nil {
		return err
	}
	security = remapSecuritySchema(security, cfg.schema)

	scopedConnection, err := prepareSchemaConnection(ctx, schemaConnectionOptions{Connection: connection, Schema: cfg.schema})
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", scopedConnection)
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
	scope := migrationScope(cfg.schema, cfg.name)
	selected, err := selectScripts(ctx, db, scope, scripts)
	if err != nil {
		return err
	}
	ordered, err := topologicalScripts(scripts, selected)
	if err != nil {
		return err
	}
	if err := runScriptPhase(ctx, db, scope, ordered, phasePre); err != nil {
		return err
	}

	client, err := sqlclient.Open(ctx, connectionWithLockTimeout(scopedConnection))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer client.Close()

	current, err := atlasmigrate.SchemaConn(client.Driver, cfg.schema, &schema.InspectOptions{Exclude: cfg.exclude}).ReadState(ctx)
	if err != nil {
		return fmt.Errorf("inspect current schema: %w", err)
	}
	desired := &schema.Realm{}
	if err := client.Eval(parser, desired, cfg.input); err != nil {
		return fmt.Errorf("evaluate HCL schemas: %w", err)
	}
	if err := remapDesiredSchema(desired, cfg.schema); err != nil {
		return fmt.Errorf("scope desired schema: %w", err)
	}

	changes, err := client.RealmDiff(current, desired)
	if err != nil {
		return fmt.Errorf("compute schema diff: %w", err)
	}
	if !cfg.allowDrops {
		changes = withoutDrops(changes)
	}
	restoreViews := noRestore
	if len(changes) == 0 {
		logger.GetLogger("migrate").Debugf("No schema changes detected")
	} else {
		invalidated, restore, err := invalidateDependentViews(ctx, db, viewInvalidationOptions{
			Scope: scope, Schema: cfg.schema, Changes: changes, Scripts: scripts,
		})
		if err != nil {
			return err
		}
		restoreViews = restore
		if len(invalidated) > 0 {
			if selected, err = selectScripts(ctx, db, scope, scripts); err != nil {
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
	if err := runScriptPhase(ctx, db, scope, ordered, phasePost); err != nil {
		return err
	}
	// Restore after the post phase: a view this scope does not own may be built
	// on top of one the post phase just recreated.
	if err := restoreViews(ctx); err != nil {
		return err
	}
	if err := retryOnLockContention(ctx, "reconcile database security", func() error {
		return reconcileSecurity(ctx, db, scope, security)
	}); err != nil {
		return fmt.Errorf("reconcile database security: %w", err)
	}
	return nil
}

// withoutDrops removes every destructive change a partial schema bundle would
// otherwise emit for objects it does not declare. Object drops are filtered
// alongside table drops because the two are load-bearing together: a bundle that
// declares neither a table nor the enum typing one of its columns emits both a
// DropTable and a DropObject, and keeping only the former leaves the column
// behind to block the DROP TYPE with "other objects depend on it".
func withoutDrops(changes []schema.Change) []schema.Change {
	log := logger.GetLogger("migrate")
	filtered := make([]schema.Change, 0, len(changes))
	for _, change := range changes {
		switch drop := change.(type) {
		case *schema.DropTable:
			log.Tracef("Skipping drop table of %s", drop.T.Name)
		case *schema.DropObject:
			log.Tracef("Skipping drop of %s", objectDescription(drop.O))
		default:
			filtered = append(filtered, change)
		}
	}
	return filtered
}

// objectDescription names a dropped object for the trace log. Enums are the only
// object Atlas's PostgreSQL driver actually plans drops for (see dropObject in
// ariga.io/atlas/sql/postgres), so anything else is reported by its Go type.
func objectDescription(object schema.Object) string {
	if enum, ok := object.(*schema.EnumType); ok {
		return fmt.Sprintf("enum type %s", enum.T)
	}
	return fmt.Sprintf("%T", object)
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
