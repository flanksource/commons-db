package snapshot

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	"github.com/flanksource/sqlglot-go/migrate"
	dialect "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

func build(c catalog) (migrate.Schema, error) {
	var out migrate.Schema
	for _, t := range c.tables {
		name := migrate.Name{Schema: t.Schema, Name: t.Name}
		if t.Type == "VIEW" {
			if t.Definition == nil || strings.TrimSpace(*t.Definition) == "" {
				return out, fmt.Errorf("view %v has no readable definition", name)
			}
			out.Definitions = append(out.Definitions, migrate.Definition{Name: name, Kind: "VIEW", SQL: *t.Definition, Dependencies: c.dependencies[t.Name]})
			continue
		}
		table, err := buildTable(c, t)
		if err != nil {
			return out, fmt.Errorf("table %v: %w", name, err)
		}
		out.Tables = append(out.Tables, table)
	}
	for _, r := range c.routines {
		if r.Definition == nil || strings.TrimSpace(*r.Definition) == "" {
			return out, fmt.Errorf("routine %s.%s has no readable definition", r.Schema, r.Name)
		}
		kind := "PROCEDURE"
		if r.Type.IsFunction() {
			kind = "FUNCTION"
		}
		out.Definitions = append(out.Definitions, migrate.Definition{Name: migrate.Name{Schema: r.Schema, Name: r.Name}, Kind: kind, SQL: *r.Definition, Dependencies: c.dependencies[r.Name]})
	}
	for _, d := range c.modules {
		d.Dependencies = c.dependencies[d.Name.Name]
		out.Definitions = append(out.Definitions, d)
	}
	return out, nil
}

func buildTable(c catalog, input mssqlcatalog.Table) (migrate.Table, error) {
	out := migrate.Table{Name: migrate.Name{Schema: input.Schema, Name: input.Name}}
	for _, col := range c.columns {
		if col.Table != input.Name {
			continue
		}
		column, err := buildColumn(col)
		if err != nil {
			return out, fmt.Errorf("column %s: %w", col.Name, err)
		}
		column.Collation = c.collations[[2]string{col.Table, col.Name}]
		out.Columns = append(out.Columns, column)
	}
	for _, index := range c.indexes {
		if index.Table != input.Name {
			continue
		}
		idx, err := buildIndex(index, c.parts)
		if err != nil {
			return out, err
		}
		out.Indexes = append(out.Indexes, idx)
	}
	for _, key := range c.keys {
		if key.Table != input.Name {
			continue
		}
		at := slices.IndexFunc(out.ForeignKeys, func(k migrate.ForeignKey) bool { return k.Name == key.Name })
		if at < 0 {
			out.ForeignKeys = append(out.ForeignKeys, migrate.ForeignKey{Name: key.Name, References: migrate.Name{Schema: key.RefSchema, Name: key.RefTable}, OnDelete: strings.ReplaceAll(key.OnDelete, "_", " "), OnUpdate: strings.ReplaceAll(key.OnUpdate, "_", " ")})
			at = len(out.ForeignKeys) - 1
		}
		out.ForeignKeys[at].Columns = append(out.ForeignKeys[at].Columns, key.Column)
		out.ForeignKeys[at].ReferencedColumns = append(out.ForeignKeys[at].ReferencedColumns, key.RefColumn)
	}
	for _, check := range c.checks {
		if check.Table != input.Name {
			continue
		}
		if check.Disabled {
			return out, fmt.Errorf("disabled check %s is unsupported", check.Name)
		}
		out.Checks = append(out.Checks, migrate.Check{Name: check.Name, Expression: check.Definition})
	}
	return out, nil
}

func buildColumn(c mssqlcatalog.Column) (migrate.Column, error) {
	out := migrate.Column{Name: c.Name, Nullable: c.Nullable}
	if c.Computed {
		return out, fmt.Errorf("computed columns are unsupported")
	}
	if c.UserDefinedType {
		return out, fmt.Errorf("user-defined column types are unsupported")
	}
	typ, err := dialect.CatalogType(dialect.CatalogColumn{Name: c.Name, DataType: c.DataType, MaxLength: c.MaxLength, Precision: c.Precision, Scale: c.Scale})
	if err != nil {
		return out, err
	}
	out.Type, err = dialect.FormatType(typ)
	if err != nil {
		return out, err
	}
	if c.Identity {
		if c.IdentitySeed == nil || c.IdentityIncrement == nil {
			return out, fmt.Errorf("identity seed and increment are missing")
		}
		out.Identity = &migrate.Identity{Seed: *c.IdentitySeed, Increment: *c.IdentityIncrement}
	}
	if c.Default != nil {
		if c.DefaultConstraint == nil || *c.DefaultConstraint == "" {
			return out, fmt.Errorf("default constraint name is missing")
		}
		out.Default = &migrate.Default{Name: *c.DefaultConstraint, Expression: *c.Default}
	}
	return out, nil
}

func buildIndex(index mssqlcatalog.Index, parts []mssqlcatalog.IndexColumn) (migrate.Index, error) {
	out := migrate.Index{Name: index.Name, Unique: index.Unique, Primary: index.Primary, Constraint: index.UniqueConstraint, Filter: index.Filter}
	switch index.TypeDesc {
	case "CLUSTERED":
		out.Clustered = true
	case "NONCLUSTERED":
	default:
		return out, fmt.Errorf("index %s has unsupported type %q", index.Name, index.TypeDesc)
	}
	for _, part := range parts {
		if part.Table != index.Table || part.Index != index.Name {
			continue
		}
		if part.Included {
			out.Included = append(out.Included, part.Column)
			continue
		}
		if part.KeyOrdinal < 1 {
			return out, fmt.Errorf("index %s has an implicit or unsupported key column %s", index.Name, part.Column)
		}
		out.Columns = append(out.Columns, migrate.IndexColumn{Name: part.Column, Descending: part.Descending})
	}
	if len(out.Columns) == 0 {
		return out, fmt.Errorf("index %s has no key columns", index.Name)
	}
	return out, nil
}
