package sqlserver

import (
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
)

// addTable creates the table with its primary key, foreign keys and checks
// inline, then its indexes.
func (s *state) addTable(add *schema.AddTable) error {
	t := add.T
	b := s.build("CREATE TABLE").Table(t)
	var errs []string
	b.WrapIndent(func(b *sqlx.Builder) {
		b.MapIndent(t.Columns, func(i int, b *sqlx.Builder) {
			if err := s.column(b, t, t.Columns[i]); err != nil {
				errs = append(errs, err.Error())
			}
		})
		if t.PrimaryKey != nil {
			s.primaryKey(b.Comma().NL(), t.PrimaryKey)
		}
		for _, fk := range t.ForeignKeys {
			s.foreignKey(b.Comma().NL(), fk)
		}
		for _, attr := range t.Attrs {
			if check, ok := attr.(*schema.Check); ok {
				checkConstraint(b.Comma().NL(), check)
			}
		}
	})
	if len(errs) > 0 {
		return fmt.Errorf("sqlserver: create table %q: %s", t.Name, strings.Join(errs, ", "))
	}
	s.append(add, fmt.Sprintf("create %q table", t.Name), b)
	for _, idx := range t.Indexes {
		if err := s.addIndex(add, t, idx); err != nil {
			return err
		}
	}
	return nil
}

// column writes a column definition: type, identity, nullability and a
// named default constraint.
func (s *state) column(b *sqlx.Builder, t *schema.Table, c *schema.Column) error {
	typ, err := FormatType(c.Type.Type)
	if err != nil {
		return fmt.Errorf("column %q: %w", c.Name, err)
	}
	b.Ident(c.Name).P(typ)
	if identity, ok := attrOf[*Identity](c.Attrs); ok {
		b.P(fmt.Sprintf("IDENTITY(%d,%d)", identity.Seed, identity.Increment))
	}
	if c.Type.Null {
		b.P("NULL")
	} else {
		b.P("NOT NULL")
	}
	if c.Default != nil {
		value, err := defaultSQL(c)
		if err != nil {
			return err
		}
		b.P("CONSTRAINT").Ident(defaultName(t, c)).P("DEFAULT", value)
	}
	return nil
}

// defaultName is the name a new default constraint is given.
func defaultName(t *schema.Table, c *schema.Column) string {
	return fmt.Sprintf("DF_%s_%s", t.Name, c.Name)
}

func (s *state) primaryKey(b *sqlx.Builder, pk *schema.Index) {
	if pk.Name != "" {
		b.P("CONSTRAINT").Ident(pk.Name)
	}
	b.P("PRIMARY KEY", clustering(pk))
	indexParts(b, pk)
}

func (s *state) foreignKey(b *sqlx.Builder, fk *schema.ForeignKey) {
	b.P("CONSTRAINT").Ident(fk.Symbol).P("FOREIGN KEY")
	b.Wrap(func(b *sqlx.Builder) {
		b.MapComma(fk.Columns, func(i int, b *sqlx.Builder) { b.Ident(fk.Columns[i].Name) })
	})
	b.P("REFERENCES").RefTable(fk.Table, fk.RefTable)
	b.Wrap(func(b *sqlx.Builder) {
		b.MapComma(fk.RefColumns, func(i int, b *sqlx.Builder) { b.Ident(fk.RefColumns[i].Name) })
	})
	if fk.OnDelete != "" {
		b.P("ON DELETE", string(fk.OnDelete))
	}
	if fk.OnUpdate != "" {
		b.P("ON UPDATE", string(fk.OnUpdate))
	}
}

func checkConstraint(b *sqlx.Builder, check *schema.Check) {
	b.P("CONSTRAINT").Ident(check.Name).P("CHECK")
	b.Wrap(func(b *sqlx.Builder) { b.WriteString(check.Expr) })
}

func clustering(idx *schema.Index) string {
	if _, ok := attrOf[*Clustered](idx.Attrs); ok {
		return "CLUSTERED"
	}
	return "NONCLUSTERED"
}

func indexParts(b *sqlx.Builder, idx *schema.Index) {
	b.Wrap(func(b *sqlx.Builder) {
		b.MapComma(idx.Parts, func(i int, b *sqlx.Builder) {
			b.Ident(idx.Parts[i].C.Name)
			if idx.Parts[i].Desc {
				b.P("DESC")
			}
		})
	})
}

// addIndex writes CREATE [UNIQUE] [NON]CLUSTERED INDEX ... INCLUDE ... WHERE.
func (s *state) addIndex(source schema.Change, t *schema.Table, idx *schema.Index) error {
	for _, part := range idx.Parts {
		if part.C == nil {
			return fmt.Errorf("sqlserver: index %s.%s: expression parts are %w", t.Name, idx.Name, errUnsupported)
		}
	}
	b := s.build("CREATE")
	if idx.Unique {
		b.P("UNIQUE")
	}
	b.P(clustering(idx), "INDEX").Ident(idx.Name).P("ON").Table(t)
	indexParts(b, idx)
	if include, ok := attrOf[*IndexInclude](idx.Attrs); ok {
		b.P("INCLUDE").Wrap(func(b *sqlx.Builder) {
			b.MapComma(include.Columns, func(i int, b *sqlx.Builder) { b.Ident(include.Columns[i]) })
		})
	}
	if predicate, ok := attrOf[*IndexPredicate](idx.Attrs); ok {
		b.P("WHERE", predicate.P)
	}
	s.append(source, fmt.Sprintf("create index %q to table: %q", idx.Name, t.Name), b)
	return nil
}

func (s *state) dropIndex(source schema.Change, t *schema.Table, idx *schema.Index) {
	if _, ok := attrOf[*UniqueConstraint](idx.Attrs); ok {
		s.dropConstraint(source, t, idx.Name)
		return
	}
	s.append(source, fmt.Sprintf("drop index %q from table: %q", idx.Name, t.Name),
		s.build("DROP INDEX").Ident(idx.Name).P("ON").Table(t))
}

func (s *state) dropConstraint(source schema.Change, t *schema.Table, name string) {
	s.append(source, fmt.Sprintf("drop constraint %q from table: %q", name, t.Name),
		s.alterTable(t).P("DROP CONSTRAINT").Ident(name))
}

func (s *state) alterTable(t *schema.Table) *sqlx.Builder {
	return s.build("ALTER TABLE").Table(t)
}
