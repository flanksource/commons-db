package sqlserver

import (
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

// Definition is the T-SQL text a routine is created from: the declared .sql
// batch for a desired routine, sys.sql_modules.definition for a live one. The
// differ compares definitions by their sqlglot canonical form, and the planner
// executes a declared definition as written.
type Definition struct {
	schema.Attr
	Text string
}

// Routine is one CREATE PROCEDURE or CREATE FUNCTION read from its definition.
type Routine struct {
	// Kind is PROCEDURE or FUNCTION.
	Kind string
	// Schema is the schema the definition names, empty when unqualified.
	Schema string
	Name   string
	// Text is the definition as written.
	Text   string
	parsed sqlglot.Routine
}

// ParseRoutine parses a single CREATE [OR ALTER] PROCEDURE|FUNCTION batch. A
// batch holding anything else, or more than one statement, is an error.
func ParseRoutine(definition string) (*Routine, error) {
	tree, err := sqlglot.ParseOne(definition, Dialect)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: parse routine: %w", err)
	}
	parsed, err := sqlglot.RoutineOf(tree)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: %w", err)
	}
	return &Routine{Kind: parsed.Kind, Schema: parsed.Schema, Name: parsed.Name, Text: definition, parsed: parsed}, nil
}

// AddTo adds the routine to s as a schema.Proc or schema.Func carrying its
// Definition. A routine qualified with another schema is an error.
func (r *Routine) AddTo(s *schema.Schema) error {
	if r.Schema != "" && !strings.EqualFold(r.Schema, s.Name) {
		return fmt.Errorf("sqlserver: routine %s.%s is declared in schema %q, not %q", r.Schema, r.Name, r.Schema, s.Name)
	}
	args, err := r.args()
	if err != nil {
		return err
	}
	attrs := []schema.Attr{&Definition{Text: r.Text}}
	if r.Kind == "PROCEDURE" {
		s.AddProcs(&schema.Proc{Name: r.Name, Schema: s, Args: args, Lang: "T-SQL", Attrs: attrs})
		return nil
	}
	ret, err := r.returns()
	if err != nil {
		return err
	}
	s.AddFuncs(&schema.Func{Name: r.Name, Schema: s, Args: args, Ret: ret, Lang: "T-SQL", Attrs: attrs})
	return nil
}

func (r *Routine) args() ([]*schema.FuncArg, error) {
	args := make([]*schema.FuncArg, 0, len(r.parsed.Params))
	for _, param := range r.parsed.Params {
		t, err := expressionType(param.Type)
		if err != nil {
			return nil, fmt.Errorf("sqlserver: routine %s parameter %s: %w", r.Name, param.Name, err)
		}
		mode := schema.FuncArgModeIn
		if param.Output {
			mode = schema.FuncArgModeInOut
		}
		args = append(args, &schema.FuncArg{Name: param.Name, Type: t, Mode: mode})
	}
	return args, nil
}

// returns is a scalar function's return type, nil for a table-valued one.
func (r *Routine) returns() (schema.Type, error) {
	if r.parsed.Returns == nil || r.parsed.Returns.Class != "DataType" {
		return nil, nil
	}
	t, err := expressionType(r.parsed.Returns)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: routine %s return type: %w", r.Name, err)
	}
	return t, nil
}

// kindNames maps sqlglot's type kinds to SQL Server type names. sqlglot folds
// REAL into FLOAT and NTEXT into TEXT, so a parameter declared with those reads
// back as float and text: parameter types are informational, never diffed.
var kindNames = map[string]string{
	"BIT": "bit", "UTINYINT": "tinyint", "SMALLINT": "smallint", "INT": "int", "BIGINT": "bigint",
	"DECIMAL": "decimal", "MONEY": "money", "SMALLMONEY": "smallmoney", "FLOAT": "float",
	"CHAR": "char", "VARCHAR": "varchar", "NCHAR": "nchar", "NVARCHAR": "nvarchar", "TEXT": "text",
	"BINARY": "binary", "VARBINARY": "varbinary", "IMAGE": "image",
	"DATE": "date", "TIME": "time", "DATETIME": "datetime", "DATETIME2": "datetime2",
	"SMALLDATETIME": "smalldatetime", "TIMESTAMPTZ": "datetimeoffset", "UUID": "uniqueidentifier",
	"XML": "xml", "VARIANT": "sql_variant", "ROWVERSION": "rowversion",
}

func expressionType(e *sqlglot.Expression) (schema.Type, error) {
	spec, err := sqlglot.DataTypeSpecOf(e, Dialect)
	if err != nil {
		return nil, err
	}
	name, ok := kindNames[spec.Kind]
	if !ok {
		return nil, fmt.Errorf("type %q is not supported", spec.Name)
	}
	return specSize(name, spec).schemaType()
}

// CreateOrAlter rewrites a CREATE PROCEDURE|FUNCTION definition to CREATE OR
// ALTER, splicing the two words in after the CREATE keyword so every other
// byte of the definition -- comments, layout, literals -- is kept.
func CreateOrAlter(definition string) (string, error) {
	tokens, err := sqlglot.Tokenize(definition, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: tokenize routine definition: %w", err)
	}
	if len(tokens) == 0 || !strings.EqualFold(tokens[0].Text, "CREATE") {
		first := ""
		if len(tokens) > 0 {
			first = tokens[0].Text
		}
		return "", fmt.Errorf("sqlserver: routine definition starts with %q, not CREATE", first)
	}
	if len(tokens) > 2 && strings.EqualFold(tokens[1].Text, "OR") && strings.EqualFold(tokens[2].Text, "ALTER") {
		return definition, nil
	}
	runes := []rune(definition)
	end := tokens[0].End + 1
	return string(runes[:end]) + " OR ALTER" + string(runes[end:]), nil
}

// canonicalDefinition is a definition's sqlglot canonical form: comments,
// whitespace, keyword case, brackets and OR ALTER normalised away.
func canonicalDefinition(name, definition string) (string, error) {
	canonical, err := sqlglot.CanonicalSQL(definition, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: routine %s: %w", name, err)
	}
	return canonical, nil
}

// definitionOf returns the Definition attribute a routine carries.
func definitionOf(name string, attrs []schema.Attr) (string, error) {
	for _, attr := range attrs {
		if d, ok := attr.(*Definition); ok {
			return d.Text, nil
		}
	}
	return "", fmt.Errorf("sqlserver: routine %s has no T-SQL definition", name)
}
