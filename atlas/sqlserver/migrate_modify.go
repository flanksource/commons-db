package sqlserver

import (
	"fmt"

	"ariga.io/atlas/sql/schema"
)

// modifyTable plans each change of a ModifyTable as its own statement.
func (s *state) modifyTable(modify *schema.ModifyTable) error {
	t := modify.T
	for _, change := range modify.Changes {
		if err := s.tableChange(t, change); err != nil {
			return fmt.Errorf("sqlserver: modify table %q: %w", t.Name, err)
		}
	}
	return nil
}

func (s *state) tableChange(t *schema.Table, change schema.Change) error {
	switch c := change.(type) {
	case *schema.AddColumn:
		return s.addColumn(c, t)
	case *schema.DropColumn:
		if err := s.dropDefault(c, t, c.C); err != nil {
			return err
		}
		s.append(c, fmt.Sprintf("drop column %q", c.C.Name), s.alterTable(t).P("DROP COLUMN").Ident(c.C.Name))
	case *schema.ModifyColumn:
		return s.modifyColumn(c, t)
	case *schema.AddIndex:
		return s.addIndex(c, t, c.I)
	case *schema.DropIndex:
		s.dropIndex(c, t, c.I)
	case *schema.ModifyIndex:
		s.dropIndex(c, t, c.From)
		return s.addIndex(c, t, c.To)
	default:
		return s.constraintChange(t, change)
	}
	return nil
}

func (s *state) constraintChange(t *schema.Table, change schema.Change) error {
	switch c := change.(type) {
	case *schema.AddPrimaryKey:
		s.addPrimaryKey(c, t, c.P)
	case *schema.DropPrimaryKey:
		s.dropConstraint(c, t, c.P.Name)
	case *schema.ModifyPrimaryKey:
		s.dropConstraint(c, t, c.From.Name)
		s.addPrimaryKey(c, t, c.To)
	case *schema.AddForeignKey:
		s.addForeignKey(c, t, c.F)
	case *schema.DropForeignKey:
		s.dropConstraint(c, t, c.F.Symbol)
	case *schema.ModifyForeignKey:
		s.dropConstraint(c, t, c.From.Symbol)
		s.addForeignKey(c, t, c.To)
	case *schema.AddCheck:
		s.addCheck(c, t, c.C)
	case *schema.DropCheck:
		s.dropConstraint(c, t, c.C.Name)
	case *schema.ModifyCheck:
		s.dropConstraint(c, t, c.From.Name)
		s.addCheck(c, t, c.To)
	default:
		return fmt.Errorf("change %T is %w", change, errUnsupported)
	}
	return nil
}

func (s *state) addColumn(c *schema.AddColumn, t *schema.Table) error {
	b := s.alterTable(t).P("ADD")
	if err := s.column(b, t, c.C); err != nil {
		return err
	}
	s.append(c, fmt.Sprintf("add column %q", c.C.Name), b)
	return nil
}

// modifyColumn alters type and nullability with ALTER COLUMN. A default
// lives in a named constraint, so a default change -- or a type change, which
// the bound default would block -- drops the old constraint and adds the new
// default as DF_<table>_<column>.
func (s *state) modifyColumn(c *schema.ModifyColumn, t *schema.Table) error {
	rebind := c.Change.Is(schema.ChangeDefault) || c.Change.Is(schema.ChangeType)
	if rebind {
		if err := s.dropDefault(c, t, c.From); err != nil {
			return err
		}
	}
	if c.Change.Is(schema.ChangeType) || c.Change.Is(schema.ChangeNull) {
		typ, err := FormatType(c.To.Type.Type)
		if err != nil {
			return fmt.Errorf("column %q: %w", c.To.Name, err)
		}
		b := s.alterTable(t).P("ALTER COLUMN").Ident(c.To.Name).P(typ, nullability(c.To))
		s.append(c, fmt.Sprintf("alter column %q", c.To.Name), b)
	}
	if rebind && c.To.Default != nil {
		value, err := defaultSQL(c.To)
		if err != nil {
			return err
		}
		b := s.alterTable(t).P("ADD CONSTRAINT").Ident(defaultName(t, c.To)).
			P("DEFAULT", value, "FOR").Ident(c.To.Name)
		s.append(c, fmt.Sprintf("set default of column %q", c.To.Name), b)
	}
	return nil
}

// dropDefault drops the named default constraint of a live column, if any.
// The inspector names every live default, so a default without its
// constraint name did not come from the database.
func (s *state) dropDefault(source schema.Change, t *schema.Table, c *schema.Column) error {
	if c.Default == nil {
		return nil
	}
	named, ok := attrOf[*DefaultConstraint](c.Attrs)
	if !ok {
		return fmt.Errorf("column %q: the live default has no constraint name to drop", c.Name)
	}
	s.dropConstraint(source, t, named.Name)
	return nil
}

func nullability(c *schema.Column) string {
	if c.Type.Null {
		return "NULL"
	}
	return "NOT NULL"
}

func (s *state) addPrimaryKey(source schema.Change, t *schema.Table, pk *schema.Index) {
	b := s.alterTable(t).P("ADD")
	s.primaryKey(b, pk)
	s.append(source, fmt.Sprintf("add primary key to table %q", t.Name), b)
}

func (s *state) addForeignKey(source schema.Change, t *schema.Table, fk *schema.ForeignKey) {
	b := s.alterTable(t).P("ADD")
	s.foreignKey(b, fk)
	s.append(source, fmt.Sprintf("add foreign key %q", fk.Symbol), b)
}

func (s *state) addCheck(source schema.Change, t *schema.Table, check *schema.Check) {
	b := s.alterTable(t).P("ADD")
	checkConstraint(b, check)
	s.append(source, fmt.Sprintf("add check %q", check.Name), b)
}
