package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/atlas/sqlserver"
)

const (
	// sqlServerSchema is the only schema SQL Server migrations manage.
	sqlServerSchema = "dbo"
	// sqlServerLockWait bounds how long a migration waits for another
	// migration of the same bundle to release its sp_getapplock.
	sqlServerLockWait = time.Minute
)

// applySQLServer reconciles the bundle's HCL tables and WithRoutines routines
// against the dbo schema. It inspects only the objects the bundle names, never
// drops a routine or an index the bundle does not declare, and runs no DDL
// when the database already matches.
// The caller owns db: closing the Atlas client would close it.
func applySQLServer(ctx context.Context, db *sql.DB, schemaFS fs.FS, cfg options) (returnErr error) {
	desired, err := sqlServerDesired(schemaFS, cfg)
	if err != nil {
		return err
	}
	client, err := sqlserver.OpenClient(ctx, db)
	if err != nil {
		return fmt.Errorf("open SQL Server migration database: %w", err)
	}
	unlock, err := client.Driver.Lock(ctx, "commons-db/migrate:"+cfg.name, sqlServerLockWait)
	if err != nil {
		return fmt.Errorf("lock SQL Server migration %q: %w", cfg.name, err)
	}
	defer func() { returnErr = errors.Join(returnErr, unlock()) }()
	return reconcileSQLServer(ctx, client, desired, cfg.exclude)
}

// sqlServerChanges inspects the declared objects and returns the changes that
// bring them to the desired state, without drops.
func sqlServerChanges(ctx context.Context, client *sqlclient.Client, desired *schema.Realm, exclude []string) ([]schema.Change, error) {
	target := desired.Schemas[0]
	current, err := client.InspectSchema(ctx, target.Name, sqlServerInspectOptions(target, exclude))
	if err != nil {
		return nil, fmt.Errorf("inspect current SQL Server schema: %w", err)
	}
	changes, err := client.RealmDiff(schema.NewRealm(current), desired)
	if err != nil {
		return nil, fmt.Errorf("compute schema diff: %w", err)
	}
	return withoutSQLServerDrops(changes), nil
}

func reconcileSQLServer(ctx context.Context, client *sqlclient.Client, desired *schema.Realm, exclude []string) error {
	changes, err := sqlServerChanges(ctx, client, desired, exclude)
	if err != nil {
		return err
	}
	log := logger.GetLogger("migrate")
	if len(changes) == 0 {
		log.Debugf("No schema changes detected")
		return nil
	}
	plan, err := client.PlanChanges(ctx, "", changes)
	if err != nil {
		return fmt.Errorf("plan %d schema changes: %w", len(changes), err)
	}
	for _, change := range plan.Changes {
		log.Tracef("%s", change.Cmd)
	}
	if err := client.ApplyChanges(ctx, changes); err != nil {
		return fmt.Errorf("apply %d schema changes: %w", len(changes), err)
	}
	log.V(1).Infof("Applied %d schema changes", len(changes))
	return nil
}

// sqlServerInspectOptions scopes inspection to the declared object names.
// SQL Server keeps tables and routines in one namespace per schema, so one
// name list filters both.
func sqlServerInspectOptions(target *schema.Schema, exclude []string) *schema.InspectOptions {
	opts := &schema.InspectOptions{Mode: schema.InspectSchemas, Exclude: exclude}
	for _, t := range target.Tables {
		opts.Tables = append(opts.Tables, t.Name)
	}
	if len(target.Tables) > 0 {
		opts.Mode |= schema.InspectTables
	}
	for _, p := range target.Procs {
		opts.Tables = append(opts.Tables, p.Name)
	}
	for _, f := range target.Funcs {
		opts.Tables = append(opts.Tables, f.Name)
	}
	if len(target.Procs)+len(target.Funcs) > 0 {
		opts.Mode |= schema.InspectFuncs
	}
	return opts
}

// withoutSQLServerDrops extends withoutDrops with the indexes of declared
// tables: indexes a DBA added to tune a shared database are left in place.
func withoutSQLServerDrops(changes []schema.Change) []schema.Change {
	log := logger.GetLogger("migrate")
	filtered := make([]schema.Change, 0, len(changes))
	for _, change := range withoutDrops(changes) {
		modify, ok := change.(*schema.ModifyTable)
		if !ok {
			filtered = append(filtered, change)
			continue
		}
		kept := make([]schema.Change, 0, len(modify.Changes))
		for _, c := range modify.Changes {
			if drop, ok := c.(*schema.DropIndex); ok {
				log.Tracef("Skipping drop index of %s.%s", modify.T.Name, drop.I.Name)
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) > 0 {
			modify.Changes = kept
			filtered = append(filtered, modify)
		}
	}
	return filtered
}

// sqlServerDesired builds the desired dbo schema from the HCL tables and the
// routine files, refusing what SQL Server migrations do not support.
func sqlServerDesired(schemaFS fs.FS, cfg options) (*schema.Realm, error) {
	if schemaFS == nil {
		return nil, errors.New("schema filesystem is nil")
	}
	if err := refuseSQLServerOptions(cfg); err != nil {
		return nil, err
	}
	scripts, err := loadScripts(schemaFS, cfg.dir, cfg.routines)
	if err != nil {
		return nil, err
	}
	if len(scripts) > 0 {
		return nil, fmt.Errorf("SQL Server migrations do not support SQL migration scripts, found %s; declare routines with WithRoutines", strings.Join(scriptPaths(scripts), ", "))
	}
	realm, err := sqlServerTables(schemaFS, cfg)
	if err != nil {
		return nil, err
	}
	target := realm.Schemas[0]
	if cfg.routines != "" {
		if err := addRoutines(schemaFS, cfg.routines, target); err != nil {
			return nil, err
		}
	}
	if len(target.Tables)+len(target.Procs)+len(target.Funcs) == 0 {
		return nil, fmt.Errorf("SQL Server migration bundle %q declares no tables or routines", cfg.name)
	}
	return realm, nil
}

func refuseSQLServerOptions(cfg options) error {
	switch {
	case cfg.schemaSet && cfg.schema != sqlServerSchema:
		return fmt.Errorf("SQL Server migrations support only schema %q, got %q", sqlServerSchema, cfg.schema)
	case cfg.allowDrops:
		return errors.New("SQL Server migrations do not support destructive changes (WithDrops)")
	case cfg.allowRebuilds:
		return errors.New("SQL Server migrations do not support table rebuilds (WithRebuilds)")
	}
	return nil
}

func addRoutines(schemaFS fs.FS, dir string, target *schema.Schema) error {
	routines, err := loadRoutines(schemaFS, dir)
	if err != nil {
		return err
	}
	for _, routine := range routines {
		if _, exists := target.Table(routine.Name); exists {
			return fmt.Errorf("routine %s has the name of a declared table", routine.Name)
		}
		if err := routine.AddTo(target); err != nil {
			return err
		}
	}
	return nil
}

// sqlServerTables evaluates the bundle's HCL, if it has any, into a realm
// holding exactly the dbo schema.
func sqlServerTables(schemaFS fs.FS, cfg options) (*schema.Realm, error) {
	found, err := hasHCL(schemaFS, cfg.dir)
	if err != nil {
		return nil, err
	}
	if !found {
		return schema.NewRealm(schema.New(sqlServerSchema)), nil
	}
	parser, security, err := loadHCL(schemaFS, cfg.dir, cfg.input)
	if err != nil {
		return nil, err
	}
	if len(security.Roles) > 0 || len(security.Permissions) > 0 {
		return nil, errors.New("SQL Server migrations do not support roles or permissions")
	}
	realm := &schema.Realm{}
	if err := sqlserver.EvalHCL.Eval(parser, realm, cfg.input); err != nil {
		return nil, fmt.Errorf("evaluate HCL schemas for SQL Server: %w", err)
	}
	if len(realm.Schemas) != 1 || realm.Schemas[0].Name != sqlServerSchema {
		return nil, fmt.Errorf("SQL Server HCL must declare exactly schema %q, got %s", sqlServerSchema, schemaNames(realm))
	}
	return realm, nil
}

func hasHCL(schemaFS fs.FS, dir string) (bool, error) {
	found := false
	err := fs.WalkDir(schemaFS, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.EqualFold(path.Ext(name), ".hcl") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("find HCL schemas in %q: %w", dir, err)
	}
	return found, nil
}

func schemaNames(realm *schema.Realm) string {
	names := make([]string, len(realm.Schemas))
	for i, s := range realm.Schemas {
		names[i] = fmt.Sprintf("%q", s.Name)
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func scriptPaths(scripts map[string]*script) []string {
	paths := make([]string, 0, len(scripts))
	for p := range scripts {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}
