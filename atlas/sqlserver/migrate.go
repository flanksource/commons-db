package sqlserver

import (
	"context"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/atlas/internal/sqlx"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

type planApply struct{ *conn }

func (*planApply) PlanChanges(ctx context.Context, name string, changes []schema.Change, options ...migrate.PlanOption) (*migrate.Plan, error) {
	return planner.DefaultPlan.PlanChanges(ctx, name, changes, options...)
}

func (p *planApply) ApplyChanges(ctx context.Context, changes []schema.Change, options ...migrate.PlanOption) error {
	return sqlx.ApplyChanges(ctx, changes, p, options...)
}
