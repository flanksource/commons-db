package sqlite

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

const (
	sqlParamMarkerPrefix      = "__cdb_query_param_"
	sqlIdentifierMarkerPrefix = "__cdb_query_identifier_"
)

var sqlParamReference = regexp.MustCompile(`(?s)\{\{\s*(?:\.params\.([A-Za-z_][A-Za-z0-9_]*)|index\s+\.params\s+"([^"]+)")\s*\}\}|\$\(\s*\.params\.([A-Za-z_][A-Za-z0-9_]*)\s*\)`)

// ParameterizeQuery replaces each params reference with a marker the dialect's
// parser reads as a plain name, so the query parses into a tree in which every
// reference is a node; the markers become bind nodes or identifiers when the
// statement is built (see materializeParams and materializeIdentifiers).
func (p sqlProvider) ParameterizeQuery(request query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	statement := request.Query
	for _, prefix := range []string{sqlParamMarkerPrefix, sqlIdentifierMarkerPrefix} {
		if strings.Contains(statement, prefix) {
			return query.ParameterizedQuery{}, fmt.Errorf("query contains reserved parameter marker %q", prefix)
		}
	}

	matches := sqlParamReference.FindAllStringSubmatchIndex(statement, -1)
	if len(matches) == 0 {
		if strings.Contains(statement, "{{") || strings.Contains(statement, "$(") {
			return query.ParameterizedQuery{}, fmt.Errorf("SQL query templates support only direct, unquoted params references")
		}
		return query.ParameterizedQuery{Query: statement}, nil
	}
	identifierParams := make(map[string]bool, len(request.Definitions))
	for _, def := range request.Definitions {
		identifierParams[def.Name] = def.Type == query.ParamTypeIdentifier
	}
	result := query.ParameterizedQuery{}
	references := map[string]string{}
	used := map[string]bool{}
	var parameterized strings.Builder
	cursor := 0
	for _, match := range matches {
		parameterized.WriteString(statement[cursor:match[0]])
		reference := statement[match[0]:match[1]]
		name := sqlParamName(statement, match)
		value, ok := request.Params[name]
		if !ok {
			return query.ParameterizedQuery{}, fmt.Errorf("references param %q, which has no value", name)
		}
		used[name] = true
		markers, err := appendSQLReference(&result, name, value, identifierParams[name])
		if err != nil {
			return query.ParameterizedQuery{}, err
		}
		for _, marker := range markers {
			references[marker] = reference
		}
		parameterized.WriteString(strings.Join(markers, ", "))
		cursor = match[1]
	}
	parameterized.WriteString(statement[cursor:])
	result.Query = parameterized.String()
	if strings.Contains(result.Query, "{{") || strings.Contains(result.Query, "$(") {
		return query.ParameterizedQuery{}, fmt.Errorf("SQL query templates support only direct, unquoted params references")
	}
	if err := p.assertBareMarkers(result.Query, references); err != nil {
		return query.ParameterizedQuery{}, err
	}
	for name := range used {
		result.UsedParams = append(result.UsedParams, name)
	}
	sort.Strings(result.UsedParams)
	return result, nil
}

// appendSQLReference records what one reference stands for on result and
// returns the markers that replace it: one identifier marker, or one value
// marker per value.
func appendSQLReference(result *query.ParameterizedQuery, name string, value any, isIdentifier bool) ([]string, error) {
	if !isIdentifier {
		var markers []string
		var err error
		result.Args, markers, err = appendSQLParamMarkers(result.Args, name, value)
		return markers, err
	}
	identifier, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("identifier param %q must resolve to a string, got %T", name, value)
	}
	if _, err := validateSQLIdentifierPath(identifier); err != nil {
		return nil, fmt.Errorf("identifier param %q: %w", name, err)
	}
	marker := sqlIdentifierMarker(len(result.Identifiers))
	result.Identifiers = append(result.Identifiers, identifier)
	return []string{marker}, nil
}

// assertBareMarkers refuses a reference written inside a literal, a quoted
// name or a comment: there it is text, not a node, and binding it would change
// what the literal says. Each marker must be a token of its own.
func (p sqlProvider) assertBareMarkers(statement string, references map[string]string) error {
	glot := ""
	if p.connType != "" {
		var err error
		if glot, err = sqlDialect(p.connType).glot(); err != nil {
			return err
		}
	}
	tokenizer, err := sqlglot.NewTokenizer(glot)
	if err != nil {
		return err
	}
	tokens, err := tokenizer.Tokenize(statement)
	if err != nil {
		return fmt.Errorf("SQL query cannot be tokenized: %w", err)
	}
	for marker, reference := range references {
		if !slices.ContainsFunc(tokens, func(token sqlglot.Token) bool {
			return token.Type == sqlglot.TokVAR && token.Text == marker
		}) {
			return fmt.Errorf("SQL parameter reference %q must not be quoted or commented", reference)
		}
	}
	return nil
}

func sqlParamName(statement string, match []int) string {
	for group := 2; group < len(match); group += 2 {
		if match[group] >= 0 {
			return statement[match[group]:match[group+1]]
		}
	}
	panic("SQL parameter reference matched without a name")
}

func appendSQLParamMarkers(args []any, name string, value any) ([]any, []string, error) {
	values := []any{value}
	if list, ok := value.([]string); ok {
		if len(list) == 0 {
			return nil, nil, fmt.Errorf("list param %q has no values", name)
		}
		values = make([]any, len(list))
		for i := range list {
			values[i] = list[i]
		}
	}
	markers := make([]string, len(values))
	for i, item := range values {
		markers[i] = sqlParamMarker(len(args))
		args = append(args, item)
	}
	return args, markers, nil
}

func sqlParamMarker(index int) string {
	return fmt.Sprintf("%s%d__", sqlParamMarkerPrefix, index)
}

func sqlIdentifierMarker(index int) string {
	return fmt.Sprintf("%s%d__", sqlIdentifierMarkerPrefix, index)
}

// materializeParams replaces each value marker with a bind node for its
// value, bound in marker order so the author's values number first.
func (s *sqlStatement) materializeParams(tree sqlNode, args []any) error {
	markers := map[string]sqlNode{}
	for _, column := range tree.FindAll("Column") {
		name := column.Name()
		if !strings.HasPrefix(name, sqlParamMarkerPrefix) || column.Args["table"] != nil {
			continue
		}
		if _, seen := markers[name]; seen {
			return fmt.Errorf("SQL query parameter marker %q appears twice", name)
		}
		markers[name] = column
	}
	for index, value := range args {
		marker := sqlParamMarker(index)
		column, ok := markers[marker]
		if !ok {
			return fmt.Errorf("SQL query parameter marker %q is missing", marker)
		}
		delete(markers, marker)
		if err := replaceNode(column, s.bind(value)); err != nil {
			return err
		}
	}
	return assertNoMarker(tree, sqlParamMarkerPrefix, "parameter")
}

// identifierSlots are the parts of a Column and a Table an identifier path
// fills, innermost first: "analytics.orders" names table orders in db
// analytics.
var identifierSlots = map[string][]string{
	"Column": {"this", "table", "db", "catalog"},
	"Table":  {"this", "db", "catalog"},
}

// materializeIdentifiers replaces each identifier marker with the quoted path
// it names, part by part.
func materializeIdentifiers(tree sqlNode, identifiers []string) error {
	for index, identifier := range identifiers {
		marker := sqlIdentifierMarker(index)
		parts, err := validateSQLIdentifierPath(identifier)
		if err != nil {
			return fmt.Errorf("SQL query identifier %d: %w", index+1, err)
		}
		found := 0
		for _, node := range tree.FindAll("Identifier") {
			if node.Name() != marker {
				continue
			}
			found++
			if err := spliceIdentifierPath(node, parts); err != nil {
				return fmt.Errorf("SQL query identifier %d: %w", index+1, err)
			}
		}
		if found == 0 {
			return fmt.Errorf("SQL query identifier marker %q is missing", marker)
		}
	}
	return assertNoMarker(tree, sqlIdentifierMarkerPrefix, "identifier")
}

// spliceIdentifierPath writes parts into the slots marker and its qualifiers
// hold. A path of one part may stand wherever a name may; a longer one only
// for a table or a column, whose qualifiers it must not overwrite.
func spliceIdentifierPath(marker sqlNode, parts []string) error {
	parent := marker.Parent
	if parent == nil {
		return fmt.Errorf("an identifier marker is the whole statement")
	}
	slots := identifierSlots[parent.Class]
	at := slices.IndexFunc(slots, func(key string) bool { return parent.Args[key] == any(marker) })
	if at < 0 {
		if len(parts) > 1 {
			return fmt.Errorf("%q is a dotted path, which only a table or a column can be", strings.Join(parts, "."))
		}
		return replaceNode(marker, identifier(parts[0]))
	}
	if at+len(parts) > len(slots) {
		return fmt.Errorf("%q has more parts than a %s name", strings.Join(parts, "."), strings.ToLower(parent.Class))
	}
	for offset := range parts {
		key := slots[at+offset]
		if offset > 0 && parent.Args[key] != nil {
			return fmt.Errorf("%q is already qualified", strings.Join(parts, "."))
		}
		parent.Set(key, identifier(parts[len(parts)-1-offset]))
	}
	return nil
}

// assertNoMarker refuses a tree in which a marker survived as text — inside a
// literal, say — rather than as the node it was replaced at.
func assertNoMarker(tree sqlNode, prefix, kind string) error {
	leaked := false
	tree.Walk(func(node sqlNode) bool {
		for _, value := range node.Args {
			if text, ok := value.(string); ok && strings.Contains(text, prefix) {
				leaked = true
			}
		}
		return !leaked
	})
	if leaked {
		return fmt.Errorf("SQL query contains an unbound %s marker", kind)
	}
	return nil
}

var _ query.QueryParameterizer = sqlProvider{}
