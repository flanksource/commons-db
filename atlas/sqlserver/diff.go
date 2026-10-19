package sqlserver

import (
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
)

// DefaultDiff provides basic diffing capabilities for SQL Server. A Driver
// opened on a connection exposes the same Differ.
var DefaultDiff schema.Differ = &sqlx.Diff{DiffDriver: &diff{}}

// diff provides a SQL Server implementation for sqlx.DiffDriver.
type diff struct{}

var _ interface {
	sqlx.DiffDriver
	sqlx.ProcFuncsDiffer
	sqlx.Normalizer
	sqlx.ChangeSupporter
} = (*diff)(nil)

// RealmObjectDiff reports no realm-level objects: the driver manages none.
func (*diff) RealmObjectDiff(_, to *schema.Realm) ([]schema.Change, error) {
	if len(to.Objects) > 0 {
		return nil, fmt.Errorf("sqlserver: realm objects are %w", errUnsupported)
	}
	return nil, nil
}

// SchemaAttrDiff reports no schema attribute changes.
func (*diff) SchemaAttrDiff(_, _ *schema.Schema) []schema.Change { return nil }

// SchemaObjectDiff refuses sequences, user-defined types and other schema
// objects, which the driver does not manage.
func (*diff) SchemaObjectDiff(_, to *schema.Schema, _ *schema.DiffOptions) ([]schema.Change, error) {
	switch {
	case len(to.Objects) > 0:
		return nil, fmt.Errorf("sqlserver: schema %s: sequences and user-defined types are %w", to.Name, errUnsupported)
	case len(to.Views) > 0:
		return nil, fmt.Errorf("sqlserver: schema %s: views are %w", to.Name, errUnsupported)
	}
	return nil, nil
}

// TableAttrDiff diffs named check constraints by their canonical expression.
func (*diff) TableAttrDiff(from, to *schema.Table, _ *schema.DiffOptions) ([]schema.Change, error) {
	if len(to.Triggers) > 0 {
		return nil, fmt.Errorf("sqlserver: table %s: triggers are %w", to.Name, errUnsupported)
	}
	canonical := make(map[*schema.Check]string)
	for _, t := range []*schema.Table{from, to} {
		for _, attr := range t.Attrs {
			check, ok := attr.(*schema.Check)
			if !ok {
				continue
			}
			expr, err := canonicalPredicate(check.Expr)
			if err != nil {
				return nil, fmt.Errorf("sqlserver: table %s check %s: %w", t.Name, check.Name, err)
			}
			canonical[check] = expr
		}
	}
	return sqlx.ChecksDiff(from, to, func(c1, c2 *schema.Check) bool {
		return canonical[c1] == canonical[c2]
	}), nil
}

// ViewAttrChanges is never reached: views are refused.
func (*diff) ViewAttrChanges(_, _ *schema.View) []schema.Change { return nil }

// ColumnChange compares nullability, canonical type spelling and canonical
// default. Identity cannot be altered in place, so an identity change is an
// error.
func (*diff) ColumnChange(t *schema.Table, from, to *schema.Column, _ *schema.DiffOptions) (schema.Change, error) {
	if err := identityUnchanged(t, from, to); err != nil {
		return sqlx.NoChange, err
	}
	var change schema.ChangeKind
	if from.Type.Null != to.Type.Null {
		change |= schema.ChangeNull
	}
	fromT, err := FormatType(from.Type.Type)
	if err != nil {
		return sqlx.NoChange, fmt.Errorf("sqlserver: column %s.%s: %w", t.Name, from.Name, err)
	}
	toT, err := FormatType(to.Type.Type)
	if err != nil {
		return sqlx.NoChange, fmt.Errorf("sqlserver: column %s.%s: %w", t.Name, to.Name, err)
	}
	if fromT != toT {
		change |= schema.ChangeType
	}
	defaultChange, err := defaultChanged(from, to)
	if err != nil {
		return sqlx.NoChange, fmt.Errorf("sqlserver: column %s.%s default: %w", t.Name, to.Name, err)
	}
	if defaultChange {
		change |= schema.ChangeDefault
	}
	if change.Is(schema.NoChange) {
		return sqlx.NoChange, nil
	}
	return &schema.ModifyColumn{From: from, To: to, Change: change}, nil
}

func identityUnchanged(t *schema.Table, from, to *schema.Column) error {
	i1, ok1 := attrOf[*Identity](from.Attrs)
	i2, ok2 := attrOf[*Identity](to.Attrs)
	if ok1 != ok2 || ok1 && (i1.Seed != i2.Seed || i1.Increment != i2.Increment) {
		return fmt.Errorf("sqlserver: column %s.%s: changing IDENTITY is %w", t.Name, to.Name, errUnsupported)
	}
	return nil
}

func defaultChanged(from, to *schema.Column) (bool, error) {
	if (from.Default == nil) != (to.Default == nil) {
		return true, nil
	}
	if from.Default == nil {
		return false, nil
	}
	d1, err := canonicalDefault(from)
	if err != nil {
		return false, err
	}
	d2, err := canonicalDefault(to)
	return err == nil && d1 != d2, err
}

func canonicalDefault(c *schema.Column) (string, error) {
	value, err := defaultSQL(c)
	if err != nil {
		return "", err
	}
	return canonicalValue(value)
}

// IndexAttrChanged compares clustering, included columns and the canonical
// filter predicate set by Normalize.
func (*diff) IndexAttrChanged(from, to []schema.Attr) bool {
	_, c1 := attrOf[*Clustered](from)
	_, c2 := attrOf[*Clustered](to)
	if c1 != c2 {
		return true
	}
	i1, _ := attrOf[*IndexInclude](from)
	i2, _ := attrOf[*IndexInclude](to)
	if !slices.EqualFunc(includeOf(i1), includeOf(i2), strings.EqualFold) {
		return true
	}
	p1, ok1 := attrOf[*IndexPredicate](from)
	p2, ok2 := attrOf[*IndexPredicate](to)
	return ok1 != ok2 || ok1 && p1.canonical() != p2.canonical()
}

func includeOf(i *IndexInclude) []string {
	if i == nil {
		return nil
	}
	return i.Columns
}

// IndexPartAttrChanged reports no index-part attribute changes.
func (*diff) IndexPartAttrChanged(_, _ *schema.Index, _ int) bool { return false }

// IsGeneratedIndexName reports false: every managed index is named.
func (*diff) IsGeneratedIndexName(*schema.Table, *schema.Index) bool { return false }

// ReferenceChanged compares referential actions, NO ACTION when unset.
func (*diff) ReferenceChanged(from, to schema.ReferenceOption) bool {
	if from == "" {
		from = schema.NoAction
	}
	if to == "" {
		to = schema.NoAction
	}
	return !strings.EqualFold(string(from), string(to))
}

// ForeignKeyAttrChanged reports no foreign-key attribute changes.
func (*diff) ForeignKeyAttrChanged(_, _ []schema.Attr) bool { return false }

// SupportChange refuses renames, which the driver never plans.
func (*diff) SupportChange(c schema.Change) bool {
	switch c.(type) {
	case *schema.RenameConstraint, *schema.RenameTable, *schema.RenameColumn, *schema.RenameIndex:
		return false
	}
	return true
}

// Normalize canonicalises the filter predicates of both tables' indexes so
// IndexAttrChanged compares them without parsing.
func (*diff) Normalize(from, to *schema.Table, _ *schema.DiffOptions) error {
	for _, t := range []*schema.Table{from, to} {
		for _, idx := range append(slices.Clip(t.Indexes), t.PrimaryKey) {
			if idx == nil {
				continue
			}
			p, ok := attrOf[*IndexPredicate](idx.Attrs)
			if !ok {
				continue
			}
			canonical, err := canonicalPredicate(p.P)
			if err != nil {
				return fmt.Errorf("sqlserver: index %s.%s filter: %w", t.Name, idx.Name, err)
			}
			p.normalized = canonical
		}
	}
	return nil
}

// ProcFuncsDiff adds the declared routines missing from the database and
// modifies those whose canonical definition differs. Routines only in the
// database are dropped; migrate filters those drops out.
func (*diff) ProcFuncsDiff(from, to *schema.Schema, _ *schema.DiffOptions) ([]schema.Change, error) {
	var changes []schema.Change
	for _, p := range to.Procs {
		change, err := procChange(from, p)
		if err != nil {
			return nil, err
		}
		changes = appendChange(changes, change)
	}
	for _, f := range to.Funcs {
		change, err := funcChange(from, f)
		if err != nil {
			return nil, err
		}
		changes = appendChange(changes, change)
	}
	for _, p := range from.Procs {
		if findProc(to, p.Name) == nil {
			changes = append(changes, &schema.DropProc{P: p})
		}
	}
	for _, f := range from.Funcs {
		if findFunc(to, f.Name) == nil {
			changes = append(changes, &schema.DropFunc{F: f})
		}
	}
	return changes, nil
}

func appendChange(changes []schema.Change, change schema.Change) []schema.Change {
	if change == nil {
		return changes
	}
	return append(changes, change)
}

func procChange(from *schema.Schema, p *schema.Proc) (schema.Change, error) {
	if findFunc(from, p.Name) != nil {
		return nil, fmt.Errorf("sqlserver: routine %s is a function in the database and a procedure in the bundle", p.Name)
	}
	live := findProc(from, p.Name)
	if live == nil {
		return &schema.AddProc{P: p}, nil
	}
	same, err := sameDefinition(p.Name, live.Attrs, p.Attrs)
	if err != nil || same {
		return nil, err
	}
	return &schema.ModifyProc{From: live, To: p}, nil
}

func funcChange(from *schema.Schema, f *schema.Func) (schema.Change, error) {
	if findProc(from, f.Name) != nil {
		return nil, fmt.Errorf("sqlserver: routine %s is a procedure in the database and a function in the bundle", f.Name)
	}
	live := findFunc(from, f.Name)
	if live == nil {
		return &schema.AddFunc{F: f}, nil
	}
	same, err := sameDefinition(f.Name, live.Attrs, f.Attrs)
	if err != nil || same {
		return nil, err
	}
	return &schema.ModifyFunc{From: live, To: f}, nil
}

// findProc and findFunc match names case-insensitively, as SQL Server's
// default collation does.
func findProc(s *schema.Schema, name string) *schema.Proc {
	i := slices.IndexFunc(s.Procs, func(p *schema.Proc) bool { return strings.EqualFold(p.Name, name) })
	if i < 0 {
		return nil
	}
	return s.Procs[i]
}

func findFunc(s *schema.Schema, name string) *schema.Func {
	i := slices.IndexFunc(s.Funcs, func(f *schema.Func) bool { return strings.EqualFold(f.Name, name) })
	if i < 0 {
		return nil
	}
	return s.Funcs[i]
}

func sameDefinition(name string, live, declared []schema.Attr) (bool, error) {
	liveText, err := definitionOf(name, live)
	if err != nil {
		return false, err
	}
	declaredText, err := definitionOf(name, declared)
	if err != nil {
		return false, err
	}
	liveCanonical, err := canonicalDefinition(name, liveText)
	if err != nil {
		return false, fmt.Errorf("live definition: %w", err)
	}
	declaredCanonical, err := canonicalDefinition(name, declaredText)
	if err != nil {
		return false, fmt.Errorf("declared definition: %w", err)
	}
	return liveCanonical == declaredCanonical, nil
}
