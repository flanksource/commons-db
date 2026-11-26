// Package sqlserver executes reviewed SQL Server migration artifacts through the sqlglot-go engine.
// It registers the SQL Server dialect, so an engine.Artifact planned for "sqlserver" can be applied or rendered.
package sqlserver

import (
	_ "github.com/flanksource/sqlglot-go/database/migrate/dialect/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	planner "github.com/flanksource/sqlglot-go/migrate/sqlserver"
)

type Request = engine.ApplyRequest

type Outcome = engine.Outcome

const Applied = engine.Applied

const AlreadyApplied = engine.AlreadyApplied

const Failed = engine.Failed

const CommitUnknown = engine.CommitUnknown

type Result = engine.Result

var Apply = engine.Apply

type ScriptOptions = planner.ScriptOptions

var RenderScript = planner.RenderScript
