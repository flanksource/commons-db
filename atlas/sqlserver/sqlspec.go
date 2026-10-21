package sqlserver

import (
	"fmt"
	"slices"

	"ariga.io/atlas/schemahcl"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlspec"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"

	"github.com/flanksource/commons-db/atlas/internal/specutil"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

// doc is the HCL document the codec reads. Only schemas and tables are
// converted; the other blocks are decoded so they can be refused by name.
// Procedures and functions come from their .sql definitions, never HCL.
type doc struct {
	Tables    []*sqlspec.Table    `spec:"table"`
	Schemas   []*sqlspec.Schema   `spec:"schema"`
	Views     []*sqlspec.View     `spec:"view"`
	Funcs     []*sqlspec.Func     `spec:"function"`
	Procs     []*sqlspec.Func     `spec:"procedure"`
	Triggers  []*sqlspec.Trigger  `spec:"trigger"`
	Sequences []*sqlspec.Sequence `spec:"sequence"`
}

func (d *doc) refuse() error {
	for kind, n := range map[string]int{
		"view": len(d.Views), "function": len(d.Funcs), "procedure": len(d.Procs),
		"trigger": len(d.Triggers), "sequence": len(d.Sequences),
	} {
		if n > 0 {
			return fmt.Errorf("sqlserver: HCL %s blocks are %w (routines come from .sql files)", kind, errUnsupported)
		}
	}
	return nil
}

// Codec for schemahcl.
type Codec struct {
	State *schemahcl.State
}

// Eval evaluates an Atlas DDL document into v using the input.
func (c *Codec) Eval(p *hclparse.Parser, v any, input map[string]cty.Value) error {
	return c.EvalOptions(p, v, &schemahcl.EvalOptions{Variables: input})
}

// EvalOptions decodes the HCL with the given options into a *schema.Realm or
// a *schema.Schema.
func (c *Codec) EvalOptions(p *hclparse.Parser, v any, opts *schemahcl.EvalOptions) error {
	var d doc
	if err := c.State.EvalOptions(p, &d, opts); err != nil {
		return err
	}
	if err := d.refuse(); err != nil {
		return err
	}
	r := &schema.Realm{}
	if err := specutil.Scan(r, &specutil.ScanDoc{Schemas: d.Schemas, Tables: d.Tables}, scanFuncs); err != nil {
		return fmt.Errorf("sqlserver: failed converting to *schema.Realm: %w", err)
	}
	switch v := v.(type) {
	case *schema.Realm:
		*v = *r
	case *schema.Schema:
		if len(r.Schemas) != 1 {
			return fmt.Errorf("sqlserver: expecting document to contain a single schema, got %d", len(r.Schemas))
		}
		*v = *r.Schemas[0]
	default:
		return fmt.Errorf("sqlserver: unexpected type %T", v)
	}
	return nil
}

// MarshalSpec marshals v into an Atlas DDL document. Routines are left out:
// their single source is the .sql definition.
func (c *Codec) MarshalSpec(v any) ([]byte, error) {
	return specutil.Marshal(v, c.State, specutil.RealmFuncs{Schema: schemaSpec})
}

var scanFuncs = &specutil.ScanFuncs{Table: convertTable}

func convertTable(spec *sqlspec.Table, parent *schema.Schema) (*schema.Table, error) {
	if err := onlyKeys(spec.Extra, nil, nil); err != nil {
		return nil, fmt.Errorf("table %q: %w", spec.Name, err)
	}
	t, err := specutil.Table(spec, parent, convertColumn, convertPrimaryKey, convertIndex, specutil.Check)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(t.Attrs, isComment) {
		return nil, fmt.Errorf("table %q: comments are %w", spec.Name, errUnsupported)
	}
	return t, nil
}

func isComment(a schema.Attr) bool {
	_, ok := a.(*schema.Comment)
	return ok
}

// onlyKeys refuses any extra attribute or block outside the allowed sets, so
// an unsupported clause (a computed column's `as`, a partition) is an error
// rather than silently dropped.
func onlyKeys(r schemahcl.Resource, attrs, blocks []string) error {
	for _, attr := range r.Attrs {
		if !slices.Contains(attrs, attr.K) {
			return fmt.Errorf("attribute %q is %w", attr.K, errUnsupported)
		}
	}
	for _, child := range r.Children {
		if !slices.Contains(blocks, child.Type) {
			return fmt.Errorf("block %q is %w", child.Type, errUnsupported)
		}
	}
	return nil
}

func convertColumn(spec *sqlspec.Column, _ *schema.Table) (*schema.Column, error) {
	if err := onlyKeys(spec.Extra, []string{"default"}, []string{"identity"}); err != nil {
		return nil, fmt.Errorf("column %q: %w", spec.Name, err)
	}
	c, err := specutil.Column(spec, convertColumnType)
	if err != nil {
		return nil, err
	}
	identity, ok := spec.Extra.Resource("identity")
	if !ok {
		return c, nil
	}
	seed, err := int64Attr(identity, "seed")
	if err != nil {
		return nil, fmt.Errorf("column %q identity: %w", spec.Name, err)
	}
	increment, err := int64Attr(identity, "increment")
	if err != nil {
		return nil, fmt.Errorf("column %q identity: %w", spec.Name, err)
	}
	return c.AddAttrs(&planner.Identity{Seed: seed, Increment: increment}), nil
}

func int64Attr(r *schemahcl.Resource, name string) (int64, error) {
	attr, ok := r.Attr(name)
	if !ok {
		return 0, fmt.Errorf("missing %q", name)
	}
	return attr.Int64()
}

func convertColumnType(spec *sqlspec.Column) (schema.Type, error) {
	return TypeRegistry.Type(spec.Type, spec.Extra.Attrs)
}

// convertPrimaryKey reads a primary key, clustered unless `clustered = false`.
func convertPrimaryKey(spec *sqlspec.PrimaryKey, t *schema.Table) (*schema.Index, error) {
	if err := onlyKeys(spec.Extra, []string{"clustered"}, nil); err != nil {
		return nil, fmt.Errorf("primary key of %q: %w", t.Name, err)
	}
	pk, err := specutil.PrimaryKey(spec, t)
	if err != nil {
		return nil, err
	}
	return pk, clusteredAttr(spec.Extra, true, pk)
}

// convertIndex reads an index, nonclustered unless `clustered = true`, with
// optional `include = [column.x]` and `where = "..."`.
func convertIndex(spec *sqlspec.Index, t *schema.Table) (*schema.Index, error) {
	if err := onlyKeys(spec.Extra, []string{"clustered", "include", "where"}, nil); err != nil {
		return nil, fmt.Errorf("index %q: %w", spec.Name, err)
	}
	idx, err := specutil.Index(spec, t)
	if err != nil {
		return nil, err
	}
	if err := clusteredAttr(spec.Extra, false, idx); err != nil {
		return nil, err
	}
	if attr, ok := spec.Extra.Attr("where"); ok {
		p, err := attr.String()
		if err != nil {
			return nil, fmt.Errorf("index %q where: %w", spec.Name, err)
		}
		idx.AddAttrs(&planner.IndexPredicate{P: p})
	}
	return idx, includeAttr(spec, t, idx)
}

func clusteredAttr(r schemahcl.Resource, fallback bool, idx *schema.Index) error {
	clustered := fallback
	if attr, ok := r.Attr("clustered"); ok {
		var err error
		if clustered, err = attr.Bool(); err != nil {
			return fmt.Errorf("index %q clustered: %w", idx.Name, err)
		}
	}
	if clustered {
		idx.AddAttrs(&planner.Clustered{})
	}
	return nil
}

func includeAttr(spec *sqlspec.Index, t *schema.Table, idx *schema.Index) error {
	attr, ok := spec.Extra.Attr("include")
	if !ok {
		return nil
	}
	refs, err := attr.Refs()
	if err != nil {
		return fmt.Errorf("index %q include: %w", spec.Name, err)
	}
	include := &planner.IndexInclude{}
	for _, ref := range refs {
		c, err := specutil.ColumnByRef(t, ref)
		if err != nil {
			return fmt.Errorf("index %q include: %w", spec.Name, err)
		}
		include.Columns = append(include.Columns, c.Name)
	}
	idx.AddAttrs(include)
	return nil
}

func schemaSpec(s *schema.Schema) (*specutil.SchemaSpec, error) {
	return specutil.FromSchema(s, &specutil.SchemaFuncs{Table: tableSpec})
}

func tableSpec(t *schema.Table) (*sqlspec.Table, error) {
	indexSpec := func(idx *schema.Index) (*sqlspec.Index, error) { return specutil.FromIndex(idx) }
	return specutil.FromTable(t, columnSpec, specutil.FromPrimaryKey, indexSpec, specutil.FromForeignKey, specutil.FromCheck)
}

func columnSpec(c *schema.Column, _ *schema.Table) (*sqlspec.Column, error) {
	return specutil.FromColumn(c, func(t schema.Type) (*sqlspec.Column, error) {
		st, err := TypeRegistry.Convert(t)
		if err != nil {
			return nil, err
		}
		return &sqlspec.Column{Type: st}, nil
	})
}

func sizedSpec(name string) *schemahcl.TypeSpec {
	return schemahcl.NewTypeSpec(name, schemahcl.WithAttributes(schemahcl.SizeTypeAttr(false)))
}

// TypeRegistry holds the SQL Server HCL type specs. A (max) size is written
// with sql(), e.g. `type = sql("nvarchar(max)")`.
var TypeRegistry = schemahcl.NewRegistry(
	schemahcl.WithFormatter(planner.FormatType),
	schemahcl.WithParser(planner.ParseType),
	schemahcl.WithSpecs(
		schemahcl.NewTypeSpec("bit"), schemahcl.NewTypeSpec("tinyint"), schemahcl.NewTypeSpec("smallint"),
		schemahcl.NewTypeSpec("int"), schemahcl.NewTypeSpec("bigint"),
		schemahcl.NewTypeSpec("decimal", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr(), schemahcl.ScaleTypeAttr())),
		schemahcl.NewTypeSpec("numeric", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr(), schemahcl.ScaleTypeAttr())),
		schemahcl.NewTypeSpec("money"), schemahcl.NewTypeSpec("smallmoney"),
		schemahcl.NewTypeSpec("float", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr())), schemahcl.NewTypeSpec("real"),
		sizedSpec("char"), sizedSpec("varchar"), sizedSpec("nchar"), sizedSpec("nvarchar"),
		schemahcl.NewTypeSpec("text"), schemahcl.NewTypeSpec("ntext"),
		sizedSpec("binary"), sizedSpec("varbinary"), schemahcl.NewTypeSpec("image"),
		schemahcl.NewTypeSpec("date"), schemahcl.NewTypeSpec("datetime"), schemahcl.NewTypeSpec("smalldatetime"),
		schemahcl.NewTypeSpec("time", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr())),
		schemahcl.NewTypeSpec("datetime2", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr())),
		schemahcl.NewTypeSpec("datetimeoffset", schemahcl.WithAttributes(schemahcl.PrecisionTypeAttr())),
		schemahcl.NewTypeSpec("uniqueidentifier"), schemahcl.NewTypeSpec("xml"),
		schemahcl.NewTypeSpec("sql_variant"), schemahcl.NewTypeSpec("rowversion"),
	),
)

var (
	codec = &Codec{
		State: schemahcl.New(
			schemahcl.WithTypes("table.column.type", TypeRegistry.Specs()),
			schemahcl.WithScopedEnums("table.foreign_key.on_update", specutil.ReferenceVars...),
			schemahcl.WithScopedEnums("table.foreign_key.on_delete", specutil.ReferenceVars...),
		),
	}
	// MarshalHCL marshals v into an Atlas HCL DDL document.
	MarshalHCL = schemahcl.MarshalerFunc(codec.MarshalSpec)
	// EvalHCL implements the schemahcl.Evaluator interface.
	EvalHCL = schemahcl.EvalFunc(codec.Eval)
	// EvalHCLBytes evaluates an HCL document from a byte slice.
	EvalHCLBytes = specutil.HCLBytesFunc(EvalHCL)
)
