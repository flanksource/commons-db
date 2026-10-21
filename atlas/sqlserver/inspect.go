package sqlserver

import (
	"context"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// inspect provides a SQL Server implementation for schema.Inspector.
type inspect struct{ *conn }

var _ schema.Inspector = (*inspect)(nil)

// managedModes is what the driver inspects. A zero mode means all of it.
const managedModes = schema.InspectSchemas | schema.InspectTables | schema.InspectFuncs

// InspectSchema returns the tables and routines of one schema, the connection's
// default schema when name is empty. opts.Tables restricts both tables and
// routines to those names.
func (i *inspect) InspectSchema(ctx context.Context, name string, opts *schema.InspectOptions) (*schema.Schema, error) {
	if opts == nil {
		opts = &schema.InspectOptions{}
	}
	mode, err := inspectMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	if name == "" {
		if name, err = i.defaultSchema(ctx); err != nil {
			return nil, err
		}
	}
	schemas, err := mssqlcatalog.Schemas(ctx, queryer{i}, []string{name})
	if err != nil {
		return nil, fmt.Errorf("sqlserver: query schemas: %w", err)
	}
	if len(schemas) == 0 {
		return nil, &schema.NotExistError{Err: fmt.Errorf("sqlserver: schema %q was not found", name)}
	}
	s := schema.New(schemas[0])
	schema.NewRealm(s)
	if err := i.inspectSchema(ctx, s, mode, opts.Tables); err != nil {
		return nil, err
	}
	return schema.ExcludeSchema(s, opts.Exclude)
}

// InspectRealm returns the tables and routines of every schema in opts.Schemas,
// or of every user schema when it is empty.
func (i *inspect) InspectRealm(ctx context.Context, opts *schema.InspectRealmOption) (*schema.Realm, error) {
	if opts == nil {
		opts = &schema.InspectRealmOption{}
	}
	mode, err := inspectMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	names, err := mssqlcatalog.Schemas(ctx, queryer{i}, opts.Schemas)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: query schemas: %w", err)
	}
	r := schema.NewRealm()
	for _, name := range names {
		s := schema.New(name)
		r.AddSchemas(s)
		if err := i.inspectSchema(ctx, s, mode, nil); err != nil {
			return nil, err
		}
	}
	return schema.ExcludeRealm(r, opts.Exclude)
}

func inspectMode(mode schema.InspectMode) (schema.InspectMode, error) {
	if mode == 0 {
		return managedModes, nil
	}
	if unmanaged := mode &^ managedModes; unmanaged != 0 {
		return 0, fmt.Errorf("sqlserver: inspect mode %d: views, triggers, types and objects are %w", unmanaged, errUnsupported)
	}
	return mode, nil
}

func (i *inspect) defaultSchema(ctx context.Context) (string, error) {
	rows, err := i.QueryContext(ctx, "SELECT SCHEMA_NAME()")
	if err != nil {
		return "", fmt.Errorf("sqlserver: query default schema: %w", err)
	}
	var name string
	if err := sqlx.ScanOne(rows, &name); err != nil {
		return "", fmt.Errorf("sqlserver: scan default schema: %w", err)
	}
	return name, nil
}

func (i *inspect) inspectSchema(ctx context.Context, s *schema.Schema, mode schema.InspectMode, names []string) error {
	scope := mssqlcatalog.Scope{Schema: s.Name, Names: names}
	if mode.Is(schema.InspectTables) {
		if err := i.inspectTables(ctx, s, scope); err != nil {
			return err
		}
	}
	if mode.Is(schema.InspectFuncs) {
		if err := i.inspectRoutines(ctx, s, scope); err != nil {
			return err
		}
	}
	return nil
}

// inspectRoutines reads each routine's definition and parses it. A routine
// whose definition is withheld or does not parse is an error.
func (i *inspect) inspectRoutines(ctx context.Context, s *schema.Schema, scope mssqlcatalog.Scope) error {
	routines, err := mssqlcatalog.Routines(ctx, queryer{i}, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query routines: %w", err)
	}
	for _, live := range routines {
		if live.Definition == nil {
			return fmt.Errorf("sqlserver: routine %s.%s has no readable definition (encrypted or no VIEW DEFINITION permission)", live.Schema, live.Name)
		}
		routine, err := planner.ParseRoutine(*live.Definition)
		if err != nil {
			return fmt.Errorf("sqlserver: live routine %s.%s: %w", live.Schema, live.Name, err)
		}
		if !strings.EqualFold(routine.Name, live.Name) {
			return fmt.Errorf("sqlserver: live routine %s.%s is defined as %q (renamed with sp_rename?)", live.Schema, live.Name, routine.Name)
		}
		routine.Name = live.Name
		if err := routine.AddTo(s); err != nil {
			return fmt.Errorf("sqlserver: live routine %s.%s: %w", live.Schema, live.Name, err)
		}
	}
	return nil
}
