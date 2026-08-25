package query

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/context"
)

// SampleResult is the bounded output used by profile authoring tools. It skips
// processors unless PreviewProcessors was explicitly requested, but always
// applies the profile's row mapping. Columns are inferred from top-level keys.
type SampleResult struct {
	Rows             []Row                `json:"rows"`
	Columns          []ColumnDef          `json:"columns"`
	ResultColumns    []ResultColumn       `json:"resultColumns"`
	RenderedQuery    string               `json:"renderedQuery"`
	Truncated        bool                 `json:"truncated,omitempty"`
	DurationMS       float64              `json:"durationMs"`
	Pagination       PageInfo             `json:"pagination"`
	Diagnostics      *ProviderDiagnostics `json:"diagnostics,omitempty"`
	ProcessorPreview *ProcessorPreview    `json:"processorPreview,omitempty"`
	Inspection       *InspectionStatus    `json:"inspection,omitempty"`
}

type SampleOptions struct {
	Params            map[string]any
	Filters           map[string]string
	FilterColumns     []ColumnDef
	Page              PageRequest
	PreviewProcessors bool
	Inspection        InspectionOptions
}

// ProcessorPreview carries the source sample and the output after each ordered
// processor. A whole-result processor sees only Input: this is a bounded preview,
// not a claim about the complete query result.
type ProcessorPreview struct {
	Input  []Row                   `json:"input"`
	Stages []ProcessorPreviewStage `json:"stages"`
}

type ProcessorPreviewStage struct {
	Index   int    `json:"index"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	RowsIn  int    `json:"rowsIn"`
	RowsOut int    `json:"rowsOut"`
	Rows    []Row  `json:"rows"`
}

// Sample renders and executes a profile through its provider while bypassing
// context queries. Processors are bypassed by default; PreviewProcessors runs
// them over the bounded raw page and records every stage. Row mapping runs only
// after that optional processor chain, and only providers whose request can be
// proven read-only are allowed.
func Sample(ctx context.Context, p Profile, options SampleOptions) (*SampleResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Kind() != KindQuery {
		return nil, fmt.Errorf("profile %q is not a single query and cannot be sampled", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	input, err := sampleInput(options.Params, options.Filters)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	filterProfile := sampleFilterProfile(p, options.FilterColumns)
	resolved, filters, err := resolveProfileInput(filterProfile, input, time.Now())
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	provider, providerErr := GetProvider(p.Provider.Type)
	req, err := buildProviderRequest(ctx, provider, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters
	// The rendered query and options are what run, so they are what must be
	// proven read-only — a templated options.method would otherwise slip a
	// non-GET request past the check.
	if err := validateSampleReadOnly(p.Provider.Type, req.Query, req.Options); err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if providerErr != nil {
		return nil, providerErr
	}
	pageRequest := options.Page
	if pageRequest.Limit <= 0 {
		pageRequest.Limit = DefaultSampleLimit
	}
	if maximum := p.RowLimits().MaxPageSize; pageRequest.Limit > maximum {
		return nil, fmt.Errorf("profile %q: requested page size %d exceeds maximum page size %d", p.Name, pageRequest.Limit, maximum)
	}
	if pageRequest.Strategy == 0 && p.Pageable() == nil && SupportsPaging(p.Provider.Type).Supports(PagingCursor) {
		pageRequest.Strategy = PagingCursor
	}
	pageRequest.Inspection = options.Inspection
	req.Inspection = options.Inspection
	// A sample explains itself when — and only as far as — the request that
	// asked for it was armed. The alternative, a flag on the sample body, let a
	// caller ask for full detail on a surface nobody was watching.
	var diagnostics *ProviderDiagnostics
	if recorder := RecorderFrom(ctx); recorder != nil {
		diagnostics = NewDiagnostics(DiagnosticOptions{
			Provider: p.Provider.Type, Query: req.Query, Options: req.Options,
			Detail: recorder.DiagnosticDetail(),
		})
		pageRequest.Diagnostics = diagnostics
	}
	started := time.Now()
	page, err := samplePage(ctx, filterProfile, input, pageRequest)
	duration := time.Since(started)
	if err != nil {
		return nil, WithDiagnostics(fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err), diagnostics)
	}
	rows := cloneSampleRows(page.Rows)
	if rows == nil {
		rows = []Row{}
	}
	rawColumns := InferSampleColumns(rows)
	var processorPreview *ProcessorPreview
	if options.PreviewProcessors {
		processorPreview, rows, err = previewSampleProcessors(ctx, p, rows)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", p.Name, err)
		}
	}
	rows, _, err = applyRowTransforms(ctx, p, rows)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if rows == nil {
		rows = []Row{}
	}
	columns, inspectionStatus, err := inspectColumns(ctx, p, req, InferSampleColumns(rows), rawColumns)
	if err != nil {
		return nil, fmt.Errorf("profile %q: inspect columns: %w", p.Name, err)
	}
	resultProfile := p
	resultProfile.Columns = columns
	resultColumns, err := DescribeResultColumns(ResultColumnOptions{Profile: resultProfile})
	if err != nil {
		return nil, fmt.Errorf("profile %q: describe result columns: %w", p.Name, err)
	}
	return &SampleResult{
		Rows:             rows,
		Columns:          columns,
		ResultColumns:    resultColumns,
		RenderedQuery:    req.Query,
		Truncated:        page.Truncated,
		DurationMS:       float64(duration) / float64(time.Millisecond),
		Pagination:       NewPageInfo(pageRequest, page),
		Diagnostics:      diagnostics.Snapshot(),
		ProcessorPreview: processorPreview,
		Inspection:       inspectionStatus,
	}, nil
}

func samplePage(ctx context.Context, profile Profile, params map[string]any, request PageRequest) (Page, error) {
	for page, err := range executeRawPages(ctx, profile, request, params) {
		return page, err
	}
	return Page{}, nil
}

func validateSampleReadOnly(providerType, query string, options map[string]any) error {
	switch providerType {
	case "sql", "postgres", "mysql", "sqlserver", "clickhouse", "sqlite":
		if err := ValidateReadOnlySQL(query); err != nil {
			return fmt.Errorf("sampling refused this query: %w", err)
		}
		return nil
	case "http":
		method := "GET"
		if raw, ok := options["method"]; ok && strings.TrimSpace(fmt.Sprint(raw)) != "" {
			method = strings.ToUpper(strings.TrimSpace(fmt.Sprint(raw)))
		}
		if method != "GET" {
			return fmt.Errorf("sampling requires a read-only HTTP GET request; method %s is not allowed", method)
		}
		return nil
	case "prometheus", "postgrest", "loki", "opensearch", "opentelemetry", "jaeger", "k8s",
		"cloudwatch", "gcpcloudlogging", "azureloganalytics":
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

// ValidateReadOnlySQL reports why sql may write, or nil when the statement can
// only read.
//
// It is deliberately the whole answer rather than a hint a caller refines: the
// question "does this statement write" cannot be answered from the opening
// keyword, because postgres spells a delete that returns rows as a WITH and
// spells an insert that runs under EXPLAIN as an EXPLAIN. Anything that decides
// what a read-only connection may run has to ask this, and nothing else.
//
// The decision is by token over a comment- and literal-aware scan, so a
// keyword inside a string or a quoted identifier is text rather than a verb,
// and a second statement after a semicolon cannot ride along behind a SELECT.
func ValidateReadOnlySQL(sql string) error {
	tokens, statements, pragmaAssignment := scanSQL(sql)
	if statements != 1 || len(tokens) == 0 {
		return fmt.Errorf("read-only execution requires exactly one SQL statement")
	}
	for _, token := range tokens {
		if _, forbidden := forbiddenSQLTokens[token]; forbidden {
			return fmt.Errorf("SQL keyword %q writes, and only read-only statements are allowed here", strings.ToUpper(token))
		}
	}
	allowed := map[string]bool{
		"select": true, "show": true, "describe": true, "desc": true,
		"explain": true, "pragma": true, "values": true, "with": true,
	}
	if !allowed[tokens[0]] {
		return fmt.Errorf("only SELECT, WITH, VALUES, SHOW, DESCRIBE, EXPLAIN, or read-only PRAGMA statements can run read-only")
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
			return fmt.Errorf("a read-only WITH statement must return rows")
		}
	}
	if tokens[0] == "pragma" && pragmaAssignment {
		return fmt.Errorf("a PRAGMA assignment writes, and only read-only statements are allowed here")
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
		if isSampleUUID(value) {
			return ColumnTypeUUID
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

// isSampleUUID recognizes an identifier by its shape rather than by its name,
// because the backends disagree on names and only one of them has a type for
// it: a postgres column reports "UUID" but an OpenSearch field holding the same
// values is mapped `keyword` like every other string.
//
// Only the canonical hyphenated form counts. uuid.Parse also accepts 32 bare
// hex digits, which is equally the shape of an MD5 digest — and a digest is a
// value someone might well want to pick from a list.
func isSampleUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
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
