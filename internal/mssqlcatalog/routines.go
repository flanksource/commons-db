package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
)

// RoutineType is the sys.objects type code of a routine.
type RoutineType string

const (
	Procedure           RoutineType = "P"
	ScalarFunction      RoutineType = "FN"
	InlineTableFunction RoutineType = "IF"
	TableFunction       RoutineType = "TF"
)

// IsFunction reports whether t is one of the function types.
func (t RoutineType) IsFunction() bool {
	return t == ScalarFunction || t == InlineTableFunction || t == TableFunction
}

// Routine is one stored procedure or T-SQL function. Definition is the
// sys.sql_modules text (nil when withheld or encrypted). ReturnType is the
// scalar return type for FN, "TABLE" for IF/TF, and nil for procedures.
type Routine struct {
	Schema     string
	Name       string
	Type       RoutineType
	Definition *string
	ReturnType *string
}

// Routines lists the procedures and T-SQL functions in the scope ordered by
// name.
func Routines(ctx context.Context, q Queryer, scope Scope) ([]Routine, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	schema := b.add(scope.Schema)
	definition := b.definition("m.definition", scope.DefinitionLimit)
	query := fmt.Sprintf(`SELECT
			s.name, o.name, RTRIM(o.type), %s,
			CASE
				WHEN o.type = 'FN' THEN (
					SELECT TOP 1 t.name
					FROM sys.parameters p
					INNER JOIN sys.types t ON p.user_type_id = t.user_type_id
					WHERE p.object_id = o.object_id AND p.parameter_id = 0
				)
				WHEN o.type IN ('IF', 'TF') THEN 'TABLE'
				ELSE NULL
			END
		FROM sys.objects o
		INNER JOIN sys.schemas s ON o.schema_id = s.schema_id
		LEFT JOIN sys.sql_modules m ON m.object_id = o.object_id
		WHERE s.name = %s AND o.type IN ('P', 'FN', 'IF', 'TF')%s
		ORDER BY o.name`, definition, schema, b.in("o.name", scope.Names))
	return collect(ctx, q, query, b.args, scanRoutine)
}

func scanRoutine(rows Rows) (Routine, error) {
	var r Routine
	var definition, returnType sql.NullString
	if err := rows.Scan(&r.Schema, &r.Name, &r.Type, &definition, &returnType); err != nil {
		return r, err
	}
	if r.Type != Procedure && !r.Type.IsFunction() {
		return r, fmt.Errorf("unexpected routine type %q for %s.%s", r.Type, r.Schema, r.Name)
	}
	r.Definition, r.ReturnType = nullableString(definition), nullableString(returnType)
	return r, nil
}

// Parameter is one declared routine parameter. A scalar function's return
// value (parameter_id 0) is excluded; Routine.ReturnType carries it.
type Parameter struct {
	Routine   string
	Name      string
	DataType  string
	Position  int
	MaxLength int
	Precision int
	Scale     int
	Output    bool
}

// Parameters lists the parameters of every routine in the scope ordered by
// routine name then parameter id.
func Parameters(ctx context.Context, q Queryer, scope Scope) ([]Parameter, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT
			o.name AS proc_name,
			p.name AS param_name,
			t.name AS data_type,
			p.parameter_id AS position,
			p.max_length,
			p.precision,
			p.scale,
			p.is_output
		FROM sys.parameters p
		INNER JOIN sys.types t ON p.user_type_id = t.user_type_id
		INNER JOIN sys.objects o ON p.object_id = o.object_id
		INNER JOIN sys.schemas s ON o.schema_id = s.schema_id
		WHERE s.name = %s
		AND o.type IN ('P', 'FN', 'IF', 'TF')
		AND p.parameter_id > 0%s
		ORDER BY o.name, p.parameter_id`, b.add(scope.Schema), b.in("o.name", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (Parameter, error) {
		var p Parameter
		return p, rows.Scan(&p.Routine, &p.Name, &p.DataType, &p.Position, &p.MaxLength, &p.Precision, &p.Scale, &p.Output)
	})
}
