package query

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/flanksource/commons-db/context"
)

const DefaultSampleLimit = 100

// SampleResult is the raw, pre-column/pre-processor output used by profile
// authoring tools. Columns are inferred only from top-level row keys.
type SampleResult struct {
	Rows          []Row       `json:"rows"`
	Columns       []ColumnDef `json:"columns"`
	RenderedQuery string      `json:"renderedQuery"`
	Truncated     bool        `json:"truncated,omitempty"`
	DurationMS    float64     `json:"durationMs"`
}

// Sample renders and executes a profile through its provider while deliberately
// bypassing configured columns, context queries and processors. Only providers
// whose request can be proven read-only are allowed.
func Sample(ctx context.Context, p Profile, params map[string]any, limit int) (*SampleResult, error) {
	if err := p.ValidateKind(); err != nil {
		return nil, err
	}
	if p.Kind() != KindQuery {
		return nil, fmt.Errorf("profile %q is not a single query and cannot be sampled", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	resolved, err := resolveParams(p.Params, params)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	rendered, err := renderQuery(ctx, p.Query, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if err := validateSampleReadOnly(p.Provider.Type, rendered, p.Provider.Options); err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	rows, err := provider.Execute(ctx, ProviderRequest{
		Connection: p.Provider.Connection,
		Query:      rendered,
		Options:    p.Provider.Options,
	})
	duration := time.Since(started)
	if err != nil {
		return nil, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err)
	}
	if limit <= 0 {
		limit = DefaultSampleLimit
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []Row{}
	}
	return &SampleResult{
		Rows:          rows,
		Columns:       InferSampleColumns(rows),
		RenderedQuery: rendered,
		Truncated:     truncated,
		DurationMS:    float64(duration) / float64(time.Millisecond),
	}, nil
}

func validateSampleReadOnly(providerType, query string, options map[string]any) error {
	switch providerType {
	case "sql", "postgres", "mysql", "sqlserver", "clickhouse":
		return validateReadOnlySQL(query)
	case "http":
		method := "GET"
		if raw, ok := options["method"]; ok && strings.TrimSpace(fmt.Sprint(raw)) != "" {
			method = strings.ToUpper(strings.TrimSpace(fmt.Sprint(raw)))
		}
		if method != "GET" {
			return fmt.Errorf("sampling requires a read-only HTTP GET request; method %s is not allowed", method)
		}
		return nil
	case "prometheus", "postgrest", "loki", "opensearch", "jaeger":
		return nil
	default:
		return fmt.Errorf("sampling provider %q is disabled because read-only execution cannot be established", providerType)
	}
}

var forbiddenSQLTokens = map[string]struct{}{
	"insert": {}, "update": {}, "delete": {}, "merge": {}, "replace": {},
	"create": {}, "alter": {}, "drop": {}, "truncate": {}, "rename": {},
	"grant": {}, "revoke": {}, "call": {}, "exec": {}, "execute": {},
	"copy": {}, "into": {}, "load": {}, "lock": {}, "vacuum": {},
	"refresh": {}, "reindex": {}, "cluster": {}, "attach": {}, "detach": {},
	"set": {}, "use": {}, "begin": {}, "commit": {}, "rollback": {},
}

func validateReadOnlySQL(sql string) error {
	tokens, statements, pragmaAssignment := scanSQL(sql)
	if statements != 1 || len(tokens) == 0 {
		return fmt.Errorf("sampling requires exactly one read-only SQL statement")
	}
	for _, token := range tokens {
		if _, forbidden := forbiddenSQLTokens[token]; forbidden {
			return fmt.Errorf("sampling rejected SQL keyword %q because only read-only statements are allowed", strings.ToUpper(token))
		}
	}
	allowed := map[string]bool{
		"select": true, "show": true, "describe": true, "desc": true,
		"explain": true, "pragma": true, "values": true, "with": true,
	}
	if !allowed[tokens[0]] {
		return fmt.Errorf("sampling only allows SELECT, WITH, VALUES, SHOW, DESCRIBE, EXPLAIN, or read-only PRAGMA statements")
	}
	if tokens[0] == "with" {
		hasResult := false
		for _, token := range tokens {
			if token == "select" || token == "values" {
				hasResult = true
				break
			}
		}
		if !hasResult {
			return fmt.Errorf("sampling requires a read-only WITH statement that returns rows")
		}
	}
	if tokens[0] == "pragma" && pragmaAssignment {
		return fmt.Errorf("sampling rejects PRAGMA assignments because only read-only statements are allowed")
	}
	return nil
}

// scanSQL returns unquoted identifier tokens, the count of non-empty
// semicolon-delimited statements, and whether a PRAGMA-like assignment appears.
// Comments and quoted strings/identifiers are ignored so embedded keywords and
// semicolons do not affect the safety decision.
func scanSQL(input string) ([]string, int, bool) {
	var tokens []string
	statements := 0
	hasToken := false
	hasAssignment := false
	for i := 0; i < len(input); {
		c := input[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if c == '-' && i+1 < len(input) && input[i+1] == '-' {
			i += 2
			for i < len(input) && input[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '*' {
			i += 2
			for i+1 < len(input) && !(input[i] == '*' && input[i+1] == '/') {
				i++
			}
			if i+1 < len(input) {
				i += 2
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			for i < len(input) {
				if input[i] == quote {
					if i+1 < len(input) && input[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				if input[i] == '\\' && i+1 < len(input) {
					i += 2
				} else {
					i++
				}
			}
			hasToken = true
			continue
		}
		if c == '[' {
			i++
			for i < len(input) && input[i] != ']' {
				i++
			}
			if i < len(input) {
				i++
			}
			hasToken = true
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(input) && (unicode.IsLetter(rune(input[j])) || unicode.IsDigit(rune(input[j])) || input[j] == '_') {
				j++
			}
			if j < len(input) && input[j] == '$' {
				delim := input[i : j+1]
				i = j + 1
				if end := strings.Index(input[i:], delim); end >= 0 {
					i += end + len(delim)
				} else {
					i = len(input)
				}
				hasToken = true
				continue
			}
		}
		if c == ';' {
			if hasToken {
				statements++
				hasToken = false
			}
			i++
			continue
		}
		if c == '=' {
			hasAssignment = true
			hasToken = true
			i++
			continue
		}
		if unicode.IsLetter(rune(c)) || c == '_' {
			j := i + 1
			for j < len(input) && (unicode.IsLetter(rune(input[j])) || unicode.IsDigit(rune(input[j])) || input[j] == '_' || input[j] == '$') {
				j++
			}
			tokens = append(tokens, strings.ToLower(input[i:j]))
			hasToken = true
			i = j
			continue
		}
		hasToken = true
		i++
	}
	if hasToken {
		statements++
	}
	return tokens, statements, hasAssignment
}

// InferSampleColumns infers stable, compact ColumnDefs from top-level row keys.
func InferSampleColumns(rows []Row) []ColumnDef {
	keys := map[string]struct{}{}
	for _, row := range rows {
		for key := range row {
			keys[key] = struct{}{}
		}
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	columns := make([]ColumnDef, 0, len(names))
	for _, name := range names {
		kind := ColumnType("")
		for _, row := range rows {
			value, ok := row[name]
			if !ok || value == nil {
				continue
			}
			next := sampleColumnType(value)
			if kind == "" {
				kind = next
			} else if kind != next {
				if isStructuredSampleType(kind) || isStructuredSampleType(next) {
					kind = ColumnTypeJSON
					continue
				}
				kind = ColumnTypeString
				break
			}
		}
		if kind == "" {
			kind = ColumnTypeString
		}
		columns = append(columns, ColumnDef{Name: name, Type: kind})
	}
	return columns
}

func sampleColumnType(value any) ColumnType {
	switch value := value.(type) {
	case time.Time, *time.Time:
		return ColumnTypeDateTime
	case string:
		if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return ColumnTypeDateTime
		}
		return ColumnTypeString
	case time.Duration:
		return ColumnTypeDuration
	case bool:
		return ColumnTypeBoolean
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return ColumnTypeNumber
	default:
		valueOf := reflect.ValueOf(value)
		if valueOf.IsValid() {
			switch valueOf.Kind() {
			case reflect.Map:
				if isFlatSampleMap(valueOf) {
					return ColumnTypeKeyValue
				}
				return ColumnTypeJSON
			case reflect.Slice, reflect.Array:
				if isSampleKeyValueList(valueOf) {
					return ColumnTypeKeyValues
				}
				return ColumnTypeJSON
			}
		}
		return ColumnTypeString
	}
}

func isStructuredSampleType(kind ColumnType) bool {
	switch kind {
	case ColumnTypeKeyValue, ColumnTypeKeyValues, ColumnTypeJSON:
		return true
	default:
		return false
	}
}

func isFlatSampleMap(value reflect.Value) bool {
	if value.Type().Key().Kind() != reflect.String {
		return false
	}
	iterator := value.MapRange()
	for iterator.Next() {
		if !isSampleScalar(iterator.Value()) {
			return false
		}
	}
	return true
}

func isSampleKeyValueList(value reflect.Value) bool {
	if value.Len() == 0 {
		return false
	}
	for i := 0; i < value.Len(); i++ {
		item := unwrapSampleValue(value.Index(i))
		if !item.IsValid() || item.Kind() != reflect.Map || item.Type().Key().Kind() != reflect.String {
			return false
		}
		hasKey, hasValue := false, false
		iterator := item.MapRange()
		for iterator.Next() {
			name := strings.ToLower(iterator.Key().String())
			switch name {
			case "key", "name":
				entry := unwrapSampleValue(iterator.Value())
				hasKey = entry.IsValid() && entry.Kind() == reflect.String
			case "value":
				hasValue = true
			}
		}
		if !hasKey || !hasValue {
			return false
		}
	}
	return true
}

func isSampleScalar(value reflect.Value) bool {
	value = unwrapSampleValue(value)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func unwrapSampleValue(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}
