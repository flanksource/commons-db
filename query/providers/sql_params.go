package providers

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/query"
)

const (
	sqlParamMarkerPrefix      = "__cdb_query_param_"
	sqlIdentifierMarkerPrefix = "__cdb_query_identifier_"
)

var sqlParamReference = regexp.MustCompile(`(?s)\{\{\s*(?:\.params\.([A-Za-z_][A-Za-z0-9_]*)|index\s+\.params\s+"([^"]+)")\s*\}\}|\$\(\s*\.params\.([A-Za-z_][A-Za-z0-9_]*)\s*\)`)

func (sqlProvider) ParameterizeQuery(request query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	statement := request.Query
	params := request.Params
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
	args := []any{}
	identifiers := []string{}
	identifierParams := make(map[string]bool, len(request.Definitions))
	for _, def := range request.Definitions {
		identifierParams[def.Name] = def.Type == query.ParamTypeIdentifier
	}
	used := map[string]bool{}
	var parameterized strings.Builder
	var err error
	cursor := 0
	for _, match := range matches {
		parameterized.WriteString(statement[cursor:match[0]])
		reference := statement[match[0]:match[1]]
		if !isBareOffset(statement, match[0]) {
			return query.ParameterizedQuery{}, fmt.Errorf("SQL parameter reference %q must not be quoted or commented", reference)
		}
		name := sqlParamName(statement, match)
		value, ok := params[name]
		if !ok {
			return query.ParameterizedQuery{}, fmt.Errorf("references param %q, which has no value", name)
		}
		used[name] = true
		if identifierParams[name] {
			identifier, ok := value.(string)
			if !ok {
				return query.ParameterizedQuery{}, fmt.Errorf("identifier param %q must resolve to a string, got %T", name, value)
			}
			if _, err := validateSQLIdentifierPath(identifier); err != nil {
				return query.ParameterizedQuery{}, fmt.Errorf("identifier param %q: %w", name, err)
			}
			parameterized.WriteString(sqlIdentifierMarker(len(identifiers)))
			identifiers = append(identifiers, identifier)
		} else {
			var markers string
			args, markers, err = appendSQLParamMarkers(args, name, value)
			if err != nil {
				return query.ParameterizedQuery{}, err
			}
			parameterized.WriteString(markers)
		}
		cursor = match[1]
	}
	parameterized.WriteString(statement[cursor:])
	result := parameterized.String()
	if strings.Contains(result, "{{") || strings.Contains(result, "$(") {
		return query.ParameterizedQuery{}, fmt.Errorf("SQL query templates support only direct, unquoted params references")
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	return query.ParameterizedQuery{
		Query: result, Args: args, Identifiers: identifiers, UsedParams: names,
	}, nil
}

func sqlParamName(statement string, match []int) string {
	for group := 2; group < len(match); group += 2 {
		if match[group] >= 0 {
			return statement[match[group]:match[group+1]]
		}
	}
	panic("SQL parameter reference matched without a name")
}

func appendSQLParamMarkers(args []any, name string, value any) ([]any, string, error) {
	values := []any{value}
	if list, ok := value.([]string); ok {
		if len(list) == 0 {
			return nil, "", fmt.Errorf("list param %q has no values", name)
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
	return args, strings.Join(markers, ", "), nil
}

func sqlParamMarker(index int) string {
	return fmt.Sprintf("%s%d__", sqlParamMarkerPrefix, index)
}

func sqlIdentifierMarker(index int) string {
	return fmt.Sprintf("%s%d__", sqlIdentifierMarkerPrefix, index)
}

func materializeSQLParams(dialect sqlDialect, statement string, args []any, identifiers []string) (string, error) {
	for index, identifier := range identifiers {
		marker := sqlIdentifierMarker(index)
		if !strings.Contains(statement, marker) {
			return "", fmt.Errorf("SQL query identifier marker %q is missing", marker)
		}
		quoted, err := dialect.quoteIdentifierPath(identifier)
		if err != nil {
			return "", fmt.Errorf("SQL query identifier %d: %w", index+1, err)
		}
		statement = strings.ReplaceAll(statement, marker, quoted)
	}
	if strings.Contains(statement, sqlIdentifierMarkerPrefix) {
		return "", fmt.Errorf("SQL query contains an unbound identifier marker")
	}
	for index := range args {
		marker := sqlParamMarker(index)
		if !strings.Contains(statement, marker) {
			return "", fmt.Errorf("SQL query parameter marker %q is missing", marker)
		}
		statement = strings.ReplaceAll(statement, marker, dialect.placeholder(index+1))
	}
	if strings.Contains(statement, sqlParamMarkerPrefix) {
		return "", fmt.Errorf("SQL query contains an unbound parameter marker")
	}
	return statement, nil
}

func sqlParamCount(statement string) (int, error) {
	count := 0
	remaining := statement
	for strings.Contains(remaining, sqlParamMarker(count)) {
		remaining = strings.ReplaceAll(remaining, sqlParamMarker(count), "")
		count++
	}
	if strings.Contains(remaining, sqlParamMarkerPrefix) {
		return 0, fmt.Errorf("SQL query contains an invalid parameter marker")
	}
	return count, nil
}

var _ query.QueryParameterizer = sqlProvider{}
