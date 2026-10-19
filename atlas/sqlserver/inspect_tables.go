package sqlserver

import (
	"context"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
)

// inspectTables reads the base tables in scope with their columns, keys,
// indexes, foreign keys and checks. A table the driver cannot reproduce --
// partitioned, triggered, or with a computed or user-defined-type column -- is
// an error.
func (i *inspect) inspectTables(ctx context.Context, s *schema.Schema, scope mssqlcatalog.Scope) error {
	q := queryer{i}
	tables, err := mssqlcatalog.Tables(ctx, q, scope, mssqlcatalog.BaseTables)
	if err != nil {
		return fmt.Errorf("sqlserver: query tables: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	scope.Names = make([]string, len(tables))
	for n, t := range tables {
		scope.Names[n] = t.Name
		s.AddTables(schema.NewTable(t.Name))
	}
	if err := refuseUnmanaged(ctx, q, scope); err != nil {
		return err
	}
	columns, err := mssqlcatalog.Columns(ctx, q, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query columns: %w", err)
	}
	if err := addColumns(s, columns); err != nil {
		return err
	}
	if err := i.inspectIndexes(ctx, s, scope); err != nil {
		return err
	}
	return i.inspectConstraints(ctx, s, scope)
}

func refuseUnmanaged(ctx context.Context, q mssqlcatalog.Queryer, scope mssqlcatalog.Scope) error {
	features, err := mssqlcatalog.UnmanagedFeatures(ctx, q, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query table features: %w", err)
	}
	if len(features) == 0 {
		return nil
	}
	found := make([]string, len(features))
	for n, f := range features {
		found[n] = fmt.Sprintf("%s.%s (%s)", scope.Schema, f.Table, f.Feature)
	}
	return fmt.Errorf("sqlserver: partitioned tables and triggers are %w: %s", errUnsupported, strings.Join(found, ", "))
}

func (i *inspect) inspectIndexes(ctx context.Context, s *schema.Schema, scope mssqlcatalog.Scope) error {
	indexes, err := mssqlcatalog.Indexes(ctx, queryer{i}, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query indexes: %w", err)
	}
	parts, err := mssqlcatalog.IndexColumns(ctx, queryer{i}, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query index columns: %w", err)
	}
	return addIndexes(s, indexes, parts)
}

func (i *inspect) inspectConstraints(ctx context.Context, s *schema.Schema, scope mssqlcatalog.Scope) error {
	fks, err := mssqlcatalog.ForeignKeys(ctx, queryer{i}, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query foreign keys: %w", err)
	}
	if err := addForeignKeys(s, fks); err != nil {
		return err
	}
	checks, err := mssqlcatalog.CheckConstraints(ctx, queryer{i}, scope)
	if err != nil {
		return fmt.Errorf("sqlserver: query check constraints: %w", err)
	}
	for _, check := range checks {
		t, err := tableOf(s, check.Table)
		if err != nil {
			return err
		}
		t.AddChecks(&schema.Check{Name: check.Name, Expr: check.Definition})
	}
	return nil
}

func tableOf(s *schema.Schema, name string) (*schema.Table, error) {
	t, ok := s.Table(name)
	if !ok {
		return nil, fmt.Errorf("sqlserver: catalog row for table %s.%s, which was not inspected", s.Name, name)
	}
	return t, nil
}

func addColumns(s *schema.Schema, columns []mssqlcatalog.Column) error {
	for _, c := range columns {
		t, err := tableOf(s, c.Table)
		if err != nil {
			return err
		}
		column, err := catalogColumn(c)
		if err != nil {
			return fmt.Errorf("sqlserver: column %s.%s.%s: %w", s.Name, c.Table, c.Name, err)
		}
		t.AddColumns(column)
	}
	return nil
}

func catalogColumn(c mssqlcatalog.Column) (*schema.Column, error) {
	switch {
	case c.Computed:
		return nil, fmt.Errorf("computed columns are %w", errUnsupported)
	case c.UserDefinedType:
		return nil, fmt.Errorf("user-defined types are %w", errUnsupported)
	}
	t, err := CatalogType(c)
	if err != nil {
		return nil, err
	}
	column := schema.NewColumn(c.Name).SetType(t).SetNull(c.Nullable)
	column.Type.Raw = c.DataType
	if c.Default != nil {
		column.Default = &schema.RawExpr{X: *c.Default}
		if c.DefaultConstraint == nil {
			return nil, fmt.Errorf("default %s has no constraint name", *c.Default)
		}
		column.AddAttrs(&DefaultConstraint{Name: *c.DefaultConstraint})
	}
	if c.Identity {
		if c.IdentitySeed == nil || c.IdentityIncrement == nil {
			return nil, fmt.Errorf("identity column without seed and increment")
		}
		column.AddAttrs(&Identity{Seed: *c.IdentitySeed, Increment: *c.IdentityIncrement})
	}
	return column, nil
}

func addIndexes(s *schema.Schema, indexes []mssqlcatalog.Index, columns []mssqlcatalog.IndexColumn) error {
	byIndex := make(map[[2]string][]mssqlcatalog.IndexColumn)
	for _, c := range columns {
		key := [2]string{c.Table, c.Index}
		byIndex[key] = append(byIndex[key], c)
	}
	for _, idx := range indexes {
		t, err := tableOf(s, idx.Table)
		if err != nil {
			return err
		}
		index, err := catalogIndex(t, idx, byIndex[[2]string{idx.Table, idx.Name}])
		if err != nil {
			return fmt.Errorf("sqlserver: index %s.%s.%s: %w", s.Name, idx.Table, idx.Name, err)
		}
		if idx.Primary {
			t.SetPrimaryKey(index)
		} else {
			t.AddIndexes(index)
		}
	}
	return nil
}

func catalogIndex(t *schema.Table, idx mssqlcatalog.Index, columns []mssqlcatalog.IndexColumn) (*schema.Index, error) {
	index := schema.NewIndex(idx.Name).SetUnique(idx.Unique).SetTable(t)
	switch idx.TypeDesc {
	case "CLUSTERED":
		index.AddAttrs(&Clustered{})
	case "NONCLUSTERED":
	default:
		return nil, fmt.Errorf("%s indexes are %w", strings.ToLower(idx.TypeDesc), errUnsupported)
	}
	if idx.Filter != "" {
		index.AddAttrs(&IndexPredicate{P: idx.Filter})
	}
	if idx.UniqueConstraint {
		index.AddAttrs(&UniqueConstraint{})
	}
	var include []string
	for _, c := range columns {
		if c.Included {
			include = append(include, c.Column)
			continue
		}
		column, ok := t.Column(c.Column)
		if !ok {
			return nil, fmt.Errorf("key column %q was not inspected", c.Column)
		}
		index.AddParts(&schema.IndexPart{SeqNo: c.KeyOrdinal, C: column, Desc: c.Descending})
	}
	if len(include) > 0 {
		index.AddAttrs(&IndexInclude{Columns: include})
	}
	return index, nil
}

func addForeignKeys(s *schema.Schema, rows []mssqlcatalog.ForeignKey) error {
	byName := make(map[[2]string]*schema.ForeignKey)
	for _, row := range rows {
		t, err := tableOf(s, row.Table)
		if err != nil {
			return err
		}
		key := [2]string{row.Table, row.Name}
		fk, ok := byName[key]
		if !ok {
			fk = &schema.ForeignKey{
				Symbol: row.Name, Table: t, RefTable: referencedTable(s, row.RefSchema, row.RefTable),
				OnDelete: referenceOption(row.OnDelete), OnUpdate: referenceOption(row.OnUpdate),
			}
			byName[key] = fk
			t.AddForeignKeys(fk)
		}
		column, ok := t.Column(row.Column)
		if !ok {
			return fmt.Errorf("sqlserver: foreign key %s.%s column %q was not inspected", row.Table, row.Name, row.Column)
		}
		fk.Columns = append(fk.Columns, column)
		fk.RefColumns = append(fk.RefColumns, referencedColumn(fk.RefTable, row.RefColumn))
	}
	return nil
}

// referencedTable is the inspected table a foreign key references, or a
// stand-in naming it when the reference leaves the inspected scope.
func referencedTable(s *schema.Schema, refSchema, name string) *schema.Table {
	if strings.EqualFold(refSchema, s.Name) {
		if t, ok := s.Table(name); ok {
			return t
		}
		return &schema.Table{Name: name, Schema: s}
	}
	return &schema.Table{Name: name, Schema: &schema.Schema{Name: refSchema}}
}

func referencedColumn(t *schema.Table, name string) *schema.Column {
	if c, ok := t.Column(name); ok {
		return c
	}
	return &schema.Column{Name: name}
}

func referenceOption(action string) schema.ReferenceOption {
	return schema.ReferenceOption(strings.ReplaceAll(action, "_", " "))
}
