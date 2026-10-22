// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
import "context"

type RoutineType = native.RoutineType

const Procedure = native.Procedure

const ScalarFunction = native.ScalarFunction

const InlineTableFunction = native.InlineTableFunction

const TableFunction = native.TableFunction

type Routine = native.Routine

func Routines(ctx context.Context, q Queryer, scope Scope) ([]Routine, error) {
	return native.Routines(ctx, q, scope)
}

type Parameter = native.Parameter

func Parameters(ctx context.Context, q Queryer, scope Scope) ([]Parameter, error) {
	return native.Parameters(ctx, q, scope)
}
