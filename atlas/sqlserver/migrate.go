package sqlserver

import (
	"context"
	"fmt"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
)

// DefaultPlan provides basic planning capabilities for SQL Server. A Driver
// opened on a connection exposes the same PlanApplier.
var DefaultPlan migrate.PlanApplier = &planApply{conn: &conn{ExecQuerier: sqlx.NoRows}}

// planApply provides migration capabilities for schema elements.
type planApply struct{ *conn }

// PlanChanges returns a migration plan for the given schema changes. Each
// statement is its own change, so a routine definition runs as its own batch.
// The statements stay valid at compatibility level 100.
func (p *planApply) PlanChanges(_ context.Context, name string, changes []schema.Change, opts ...migrate.PlanOption) (*migrate.Plan, error) {
	s := &state{Plan: migrate.Plan{Name: name}}
	for _, o := range opts {
		o(&s.PlanOptions)
	}
	if err := s.plan(changes); err != nil {
		return nil, err
	}
	return &s.Plan, nil
}

// ApplyChanges plans the changes and executes each statement in order.
func (p *planApply) ApplyChanges(ctx context.Context, changes []schema.Change, opts ...migrate.PlanOption) error {
	return sqlx.ApplyChanges(ctx, changes, p, opts...)
}

// state is one planning run.
type state struct {
	migrate.Plan
	migrate.PlanOptions
}

func (s *state) plan(changes []schema.Change) error {
	if s.SchemaQualifier != nil {
		if err := sqlx.CheckChangesScope(s.PlanOptions, changes); err != nil {
			return err
		}
	}
	planned, err := sqlx.DetachCycles(changes)
	if err != nil {
		return err
	}
	for _, c := range sqlx.SortChanges(planned, nil) {
		if err := s.planChange(c); err != nil {
			return err
		}
	}
	return nil
}

func (s *state) planChange(c schema.Change) error {
	switch c := c.(type) {
	case *schema.AddTable:
		return s.addTable(c)
	case *schema.DropTable:
		s.append(c, fmt.Sprintf("drop %q table", c.T.Name), s.build("DROP TABLE").Table(c.T))
		return nil
	case *schema.ModifyTable:
		return s.modifyTable(c)
	case *schema.AddProc:
		return s.createOrAlter(c, c.P.Name, c.P.Attrs)
	case *schema.ModifyProc:
		return s.createOrAlter(c, c.To.Name, c.To.Attrs)
	case *schema.AddFunc:
		return s.createOrAlter(c, c.F.Name, c.F.Attrs)
	case *schema.ModifyFunc:
		return s.createOrAlter(c, c.To.Name, c.To.Attrs)
	case *schema.DropProc:
		s.append(c, fmt.Sprintf("drop %q procedure", c.P.Name), s.build("DROP PROCEDURE").Proc(c.P))
		return nil
	case *schema.DropFunc:
		s.append(c, fmt.Sprintf("drop %q function", c.F.Name), s.build("DROP FUNCTION").Func(c.F))
		return nil
	}
	return fmt.Errorf("sqlserver: change %T is %w", c, errUnsupported)
}

// createOrAlter runs the declared definition itself, as CREATE OR ALTER.
func (s *state) createOrAlter(c schema.Change, name string, attrs []schema.Attr) error {
	text, err := definitionOf(name, attrs)
	if err != nil {
		return err
	}
	cmd, err := CreateOrAlter(text)
	if err != nil {
		return fmt.Errorf("sqlserver: routine %s: %w", name, err)
	}
	s.Changes = append(s.Changes, &migrate.Change{Cmd: cmd, Source: c, Comment: fmt.Sprintf("create or alter %q", name)})
	return nil
}

func (s *state) build(phrases ...string) *sqlx.Builder {
	b := &sqlx.Builder{QuoteOpening: '[', QuoteClosing: ']', Schema: s.SchemaQualifier, Indent: s.Indent}
	return b.P(phrases...)
}

func (s *state) append(source schema.Change, comment string, b *sqlx.Builder) {
	s.Changes = append(s.Changes, &migrate.Change{Cmd: b.String(), Source: source, Comment: comment})
}
