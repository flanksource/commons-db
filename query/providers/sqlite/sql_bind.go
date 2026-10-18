package sqlite

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// clickHouseBindPrefix names ClickHouse's bind markers, @cdb_p1..@cdb_pN.
//
// clickhouse-go binds client-side and reads a bare "?" as positional, but the
// ClickHouse generator writes a Placeholder as the server-side `{?: }` form,
// which no value fills. The marker it does write that the driver binds is a
// Parameter, `@name`, filled from named arguments — so ClickHouse binds by
// name, and driverArgs names each argument after its marker.
const clickHouseBindPrefix = "cdb_p"

// positionalProbePrefix names each positional marker while its position in
// the written statement is found; see positionalArgs.
const positionalProbePrefix = "cdb_probe_"

var positionalProbe = regexp.MustCompile(`:` + positionalProbePrefix + `([0-9]+)`)

// glot is the sqlglot dialect that parses and writes d's SQL.
func (d sqlDialect) glot() (string, error) {
	switch d {
	case dialectPostgres:
		return "postgres", nil
	case dialectMySQL:
		return "mysql", nil
	case dialectSQLServer:
		return "tsql", nil
	case dialectClickHouse:
		return "clickhouse", nil
	case dialectSQLite:
		return "sqlite", nil
	default:
		return "", fmt.Errorf("unsupported sql connection type %q", d)
	}
}

// sqlStatement is a statement under construction: its tree, and the values
// its bind nodes stand for, in the order they were bound.
type sqlStatement struct {
	dialect sqlDialect
	tree    sqlNode
	binds   []sqlNode
	args    []any
}

func newSQLStatement(dialect sqlDialect) (*sqlStatement, error) {
	if _, err := dialect.glot(); err != nil {
		return nil, err
	}
	return &sqlStatement{dialect: dialect}, nil
}

// bind records value and returns the node that marks it in the statement:
// $N for postgres and @pN for SQL Server, numbered in bind order; @cdb_pN for
// ClickHouse, named in bind order; a bare "?" elsewhere, whose arguments
// render orders by where the generator wrote them.
func (s *sqlStatement) bind(value any) sqlNode {
	s.args = append(s.args, value)
	index := len(s.args)
	var marker sqlNode
	switch s.dialect {
	case dialectPostgres:
		marker = sqlglot.New("Parameter", kv("this", number(index)))
	case dialectSQLServer:
		marker = sqlglot.New("Parameter", kv("this", sqlglot.New("Var", kv("this", "p"+strconv.Itoa(index)))))
	case dialectClickHouse:
		marker = sqlglot.New("Parameter", kv("this", sqlglot.New("Var", kv("this", clickHouseBindName(index)))))
	default:
		marker = sqlglot.New("Placeholder")
	}
	s.binds = append(s.binds, marker)
	return marker
}

func clickHouseBindName(index int) string { return clickHouseBindPrefix + strconv.Itoa(index) }

// render writes the tree in the statement's dialect, with the arguments in the
// order the driver reads them.
func (s *sqlStatement) render() (string, []any, error) {
	glot, err := s.dialect.glot()
	if err != nil {
		return "", nil, err
	}
	args := s.args
	if s.dialect == dialectMySQL || s.dialect == dialectSQLite {
		if args, err = s.positionalArgs(glot); err != nil {
			return "", nil, err
		}
	}
	statement, err := sqlglot.Generate(s.tree, glot)
	if err != nil {
		return "", nil, fmt.Errorf("write the %s statement: %w", glot, err)
	}
	return statement, args, nil
}

// positionalArgs orders the arguments of a "?" dialect by where the generator
// writes their markers, which need not be the order they were bound in: the
// generator writes clauses in its own canonical order.
//
// A copy of the tree with every marker named is written once, and the names
// are read back in the order they appear. The copy is never run; the statement
// a driver runs is the unnamed tree's.
func (s *sqlStatement) positionalArgs(glot string) ([]any, error) {
	markers := s.tree.FindAll("Placeholder")
	if len(markers) != len(s.binds) {
		return nil, fmt.Errorf("the statement holds %d bind markers for %d values", len(markers), len(s.binds))
	}
	bound := make(map[sqlNode]int, len(s.binds))
	for index, marker := range s.binds {
		bound[marker] = index
	}
	probe := s.tree.Copy()
	for at, marker := range probe.FindAll("Placeholder") {
		index, ok := bound[markers[at]]
		if !ok {
			return nil, fmt.Errorf("the statement holds a bind marker no value was bound to")
		}
		marker.Set("this", positionalProbePrefix+strconv.Itoa(index))
	}
	written, err := sqlglot.Generate(probe, glot)
	if err != nil {
		return nil, fmt.Errorf("write the %s statement: %w", glot, err)
	}
	return argsInWrittenOrder(written, s.args)
}

func argsInWrittenOrder(written string, args []any) ([]any, error) {
	found := positionalProbe.FindAllStringSubmatch(written, -1)
	if len(found) != len(args) {
		return nil, fmt.Errorf("the written statement holds %d bind markers for %d values", len(found), len(args))
	}
	ordered := make([]any, len(args))
	seen := make([]bool, len(args))
	for at, match := range found {
		index, err := strconv.Atoi(match[1])
		if err != nil || index >= len(args) || seen[index] {
			return nil, fmt.Errorf("the written statement repeats or invents bind marker %q", match[0])
		}
		seen[index] = true
		ordered[at] = args[index]
	}
	return ordered, nil
}

// driverArgs is args as dialect's driver takes them: named after their
// markers for ClickHouse, positional everywhere else.
func driverArgs(dialect sqlDialect, args []any) []any {
	if dialect != dialectClickHouse {
		return args
	}
	named := make([]any, len(args))
	for index, value := range args {
		named[index] = sql.Named(clickHouseBindName(index+1), value)
	}
	return named
}
