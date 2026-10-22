package sqlserver

import (
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	runtime "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

const Dialect = planner.Dialect

type Identity = planner.Identity
type DefaultConstraint = planner.DefaultConstraint
type Clustered = planner.Clustered
type IndexInclude = planner.IndexInclude
type IndexPredicate = planner.IndexPredicate
type UniqueConstraint = planner.UniqueConstraint
type SpecialType = planner.SpecialType
type Definition = planner.Definition
type Routine = planner.Routine

var DefaultDiff schema.Differ = planner.DefaultDiff
var DefaultPlan migrate.PlanApplier = runtime.DefaultPlan

func ParseType(input string) (schema.Type, error) { return planner.ParseType(input) }

func FormatType(input schema.Type) (string, error) { return planner.FormatType(input) }

func ParseRoutine(input string) (*Routine, error) { return planner.ParseRoutine(input) }

func CreateOrAlter(input string) (string, error) { return planner.CreateOrAlter(input) }
