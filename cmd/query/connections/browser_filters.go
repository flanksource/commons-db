package connections

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

type browserColumnFilter = query.ResultColumnFilter

type browserFilterOption struct {
	Value string `json:"value"`
	Count int64  `json:"count,omitempty"`
}

// browserFilterValuesRequest asks what a filterable column holds, scoped to the
// query being browsed and the filters already picked — so a suggestion list
// only offers values that would still return rows.
type browserFilterValuesRequest struct {
	Query             string            `json:"query"`
	Options           map[string]any    `json:"options,omitempty"`
	Columns           []browserColumn   `json:"columns,omitempty"`
	Filters           map[string]string `json:"filters,omitempty"`
	FilterKey         string            `json:"filterKey"`
	Search            string            `json:"search,omitempty"`
	Limit             int               `json:"limit,omitempty"`
	RefreshInspection bool              `json:"refreshInspection,omitempty"`
}

type browserFilterValuesResult struct {
	Options []browserFilterOption `json:"options"`
	Total   int                   `json:"total,omitempty"`

	// TotalRelation says how to read Total, in the same vocabulary the profile
	// export headers use: "eq", "gte" or "unknown". OpenSearch stops counting
	// past its tracking threshold and reports a lower bound; rendering that as
	// a count states a number nobody promised.
	TotalRelation string `json:"totalRelation,omitempty"`

	Truncated bool `json:"truncated,omitempty"`
}

const (
	browserFilterValueLimit = 20
	browserFilterValueMax   = 200
)

// sqlColumnTypeFamilies maps the type names a driver reports onto the column
// types the filter model reasons about. Only these families are filterable; an
// unmapped type gets no filter rather than one that means something else.
var sqlColumnTypeFamilies = map[string]query.ColumnType{
	"BOOL": query.ColumnTypeBoolean, "BOOLEAN": query.ColumnTypeBoolean, "BIT": query.ColumnTypeBoolean,

	"INT": query.ColumnTypeNumber, "INT2": query.ColumnTypeNumber, "INT4": query.ColumnTypeNumber,
	"INT8": query.ColumnTypeNumber, "INTEGER": query.ColumnTypeNumber, "BIGINT": query.ColumnTypeNumber,
	"SMALLINT": query.ColumnTypeNumber, "TINYINT": query.ColumnTypeNumber, "SERIAL": query.ColumnTypeNumber,
	"FLOAT": query.ColumnTypeNumber, "FLOAT4": query.ColumnTypeNumber, "FLOAT8": query.ColumnTypeNumber,
	"DOUBLE": query.ColumnTypeNumber, "REAL": query.ColumnTypeNumber, "DECIMAL": query.ColumnTypeNumber,
	"NUMERIC": query.ColumnTypeNumber, "MONEY": query.ColumnTypeNumber,

	"TIMESTAMP": query.ColumnTypeDateTime, "TIMESTAMPTZ": query.ColumnTypeDateTime,
	"DATETIME": query.ColumnTypeDateTime, "DATETIME2": query.ColumnTypeDateTime,
	"DATE": query.ColumnTypeDateTime, "TIME": query.ColumnTypeDateTime,

	"TEXT": query.ColumnTypeString, "VARCHAR": query.ColumnTypeString, "CHAR": query.ColumnTypeString,
	"BPCHAR": query.ColumnTypeString, "NVARCHAR": query.ColumnTypeString, "NCHAR": query.ColumnTypeString,
	"NAME": query.ColumnTypeString, "ENUM": query.ColumnTypeString,
	"STRING": query.ColumnTypeString,

	// An identifier compares exactly like a string; it is typed apart because
	// enumerating one is a scan that answers with a page of the rows.
	"UUID": query.ColumnTypeUUID, "UNIQUEIDENTIFIER": query.ColumnTypeUUID,
}

// sqlBrowserColumns describes a SQL result set from the types the driver
// reported.
//
// The types come from the result rather than from the catalog because the
// filter wraps the statement in a CTE and narrows its *output*: an alias or an
// expression is filterable by the name it was given, and no catalog knows those
// names. A duplicated output name gets no filter, because a filter key names
// one column and two columns answering to it would be a coin toss.
func sqlBrowserColumns(columnTypes []*sql.ColumnType) []query.ColumnDef {
	seen := map[string]int{}
	for _, column := range columnTypes {
		seen[column.Name()]++
	}
	columns := make([]query.ColumnDef, 0, len(columnTypes))
	for _, column := range columnTypes {
		def := query.ColumnDef{Name: column.Name(), Type: sqlColumnTypeOf(column)}
		if column.Name() == "" || seen[column.Name()] > 1 || def.Type == "" {
			def.Filter = &query.ColumnFilterDef{Disabled: true}
		}
		columns = append(columns, def)
	}
	return columns
}

// sqlColumnTypeOf resolves how a result column's values compare.
//
// The driver's type name is read first because it is the more specific of the
// two signals, and the Go type it scans into second — a user-defined type (a
// postgres enum, say) has no name in any driver's static table and arrives as a
// bare OID, but it still scans into the Go type it behaves like.
func sqlColumnTypeOf(column *sql.ColumnType) query.ColumnType {
	if named := sqlColumnType(column.DatabaseTypeName()); named != "" {
		return named
	}
	return sqlScanColumnType(column.ScanType())
}

// sqlScanColumnType reads how values compare from the Go type they scan into.
func sqlScanColumnType(scanType reflect.Type) query.ColumnType {
	if scanType == nil {
		return ""
	}
	if scanType == reflect.TypeOf(time.Time{}) {
		return query.ColumnTypeDateTime
	}
	switch scanType.Kind() {
	case reflect.String:
		return query.ColumnTypeString
	case reflect.Bool:
		return query.ColumnTypeBoolean
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return query.ColumnTypeNumber
	default:
		// An interface, a struct or a slice says only that the driver hands back
		// something — not how two of them compare.
		return ""
	}
}

// sqlColumnTypeName is the type name worth showing beside a column. A driver
// that has no name for a type reports its numeric OID, which tells a reader
// nothing, so it is not shown at all.
func sqlColumnTypeName(databaseType string) string {
	name := strings.TrimSpace(databaseType)
	if name == "" || strings.IndexFunc(name, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		return ""
	}
	return name
}

// sqlColumnType reads the driver's type name, which arrives decorated in ways
// that say nothing about how a value compares — a length, an array marker, an
// unsigned flag.
func sqlColumnType(databaseType string) query.ColumnType {
	name := strings.ToUpper(strings.TrimSpace(databaseType))
	name = strings.TrimPrefix(name, "_")
	if at := strings.IndexByte(name, '('); at >= 0 {
		name = name[:at]
	}
	name = strings.TrimSuffix(name, "[]")
	for _, suffix := range []string{" UNSIGNED", " WITHOUT TIME ZONE", " WITH TIME ZONE"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return sqlColumnTypeFamilies[strings.TrimSpace(name)]
}

// browserProfile assembles the profile a browsed query would be if it were
// stored, so the filter model that serves every stored profile serves the
// console too rather than growing a parallel implementation for it.
func browserProfile(
	descriptor browserDescriptor,
	conn *models.Connection,
	statement string,
	options map[string]any,
	columns []query.ColumnDef,
) query.Profile {
	profileOptions := map[string]any{}
	for key, value := range options {
		profileOptions[key] = value
	}
	return query.Profile{
		Name: "connection browser",
		Provider: query.ProviderConfig{
			Type:       descriptor.Provider,
			Connection: conn.ID.String(),
			Options:    profileOptions,
		},
		Query:   statement,
		Columns: columns,
	}
}

// browserColumnDefs reads back the column set the console was offered.
//
// It is the inverse of describeBrowserColumns, and the two must stay inverses:
// the browser echoes what it received, so anything this does not read back is a
// filter the console offered and the server then declines to compile.
func browserColumnDefs(columns []browserColumn) []query.ColumnDef {
	defs := make([]query.ColumnDef, 0, len(columns))
	for _, column := range columns {
		def := query.ColumnDef{Name: column.Name}
		if column.Filter == nil {
			def.Filter = &query.ColumnFilterDef{Disabled: true}
		} else {
			lookup, multi := column.Filter.Lookup, column.Filter.Multi
			options := make([]string, 0, len(column.Filter.Options))
			for _, option := range column.Filter.Options {
				options = append(options, option.Value)
			}
			def.Filter = &query.ColumnFilterDef{
				Kind:    query.ColumnFilterKind(column.Filter.Kind),
				Lookup:  &lookup,
				Multi:   &multi,
				Options: options,
			}
		}
		defs = append(defs, def)
	}
	return defs
}

// browserFilterColumns resolves the columns a selection binds to.
//
// A SQL result is described by the run that produced it — its columns are the
// author's SELECT aliases, which exist in no catalog — so the console echoes
// back what it was offered. An index has a mapping, which is the authority on
// how a field compares and whether it can be enumerated, so nothing is echoed
// there: the mapping is read.
func (h *connectionBrowserHandler) browserFilterColumns(
	ctx dbcontext.Context,
	conn *models.Connection,
	descriptor browserDescriptor,
	options map[string]any,
	echoed []browserColumn,
	requested []string,
	refresh bool,
) ([]query.ColumnDef, error) {
	if descriptor.Provider != "opensearch" {
		return browserColumnDefs(echoed), nil
	}
	index, _ := options["index"].(string)
	if index == "" {
		return nil, fmt.Errorf("OpenSearch index is required")
	}
	searcher, err := h.openSearchSearcher(ctx, conn)
	if err != nil {
		return nil, err
	}
	fields, err := h.openSearchFieldCatalog(ctx.Context, searcher, index, refresh)
	if err != nil {
		return nil, err
	}
	shape, err := h.openSearchSampleShape(ctx.Context, searcher, index, fields)
	if err != nil {
		return nil, err
	}
	return openSearchFilterColumns(fields, shape, requested), nil
}

// browserFilterColumnNames reads the columns a selection binds to out of its
// request keys, which carry the same "filter." prefix a stored profile's do.
func browserFilterColumnNames(filters map[string]string) []string {
	names := make([]string, 0, len(filters))
	for key := range filters {
		names = append(names, strings.TrimPrefix(key, "filter."))
	}
	sort.Strings(names)
	return names
}

// browserFilterInput turns the browser's flat selection into the input the
// profile resolver reads, which is the same shape a query string arrives in.
func browserFilterInput(filters map[string]string) map[string]any {
	input := make(map[string]any, len(filters))
	for key, value := range filters {
		if strings.TrimSpace(value) != "" {
			input[key] = value
		}
	}
	return input
}

// resolveBrowserFilters resolves the browser's selection against the columns it
// was offered.
//
// The columns are echoed back by the browser rather than re-derived here: a
// filter must bind to the field and kind the console actually offered, and a
// re-derivation from a *filtered* result would not necessarily agree.
func resolveBrowserFilters(profile query.Profile, filters map[string]string) ([]query.ColumnFilterValue, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	if len(profile.Columns) == 0 {
		return nil, fmt.Errorf(
			"filters were sent without the columns they bind to; run the query once to learn what it returns")
	}
	resolved, err := query.ResolveColumnFilters(profile, browserFilterInput(filters))
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// serveFilterValues answers a filter's value type-ahead for a browsed query.
//
// It is its own route rather than an overload of /values: that one is scoped to
// a structured search specification and refuses a non-OpenSearch connection, so
// one struct serving both questions would leave half its fields permanently
// ignored. The implementation is shared — this routes to the same
// FilterLookupProvider a stored profile's lookup uses.
func (h *connectionBrowserHandler) serveFilterValues(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	descriptor, ok := descriptorForConnection(conn.Type)
	if !ok || descriptor.Kind != "query" {
		http.Error(w, "connection does not support queries", http.StatusBadRequest)
		return
	}
	var request browserFilterValuesRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "decode filter values request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.FilterKey) == "" {
		http.Error(w, "filterKey is required", http.StatusBadRequest)
		return
	}
	limit := request.Limit
	if limit <= 0 {
		limit = browserFilterValueLimit
	}
	if limit > browserFilterValueMax {
		limit = browserFilterValueMax
	}
	for _, key := range []string{"url", "address", "type"} {
		delete(request.Options, key)
	}
	ctx := devtools.WithRequestRecorder(h.ctx.Wrap(r.Context()), r)

	requested := append(browserFilterColumnNames(request.Filters), strings.TrimPrefix(request.FilterKey, "filter."))
	columns, err := h.browserFilterColumns(ctx, conn, descriptor, request.Options, request.Columns, requested, request.RefreshInspection)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	profile := browserProfile(descriptor, conn, request.Query, request.Options, columns)
	options, total, err := query.LookupFilterValues(ctx, query.FilterValueLookupRequest{
		Profile: profile, Input: browserFilterInput(request.Filters),
		Key: request.FilterKey, Search: request.Search, Limit: limit,
		Inspection: query.InspectionOptions{Refresh: request.RefreshInspection},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	result := browserFilterValuesResult{
		Options:       make([]browserFilterOption, 0, len(options)),
		TotalRelation: total.Relation(),
	}
	if total != nil {
		result.Total = int(total.Value)
		result.Truncated = int(total.Value) > len(options)
	}
	for _, option := range options {
		result.Options = append(result.Options, browserFilterOption{Value: option.Value, Count: option.Count})
	}
	writeJSON(w, result)
}

// describeBrowserColumns renders the column set the browser reads: how to
// display each column and, where the provider can narrow on it, which key to
// send the selection under.
func describeBrowserColumns(profile query.Profile, databaseTypes map[string]string) ([]browserColumn, error) {
	return query.DescribeResultColumns(query.ResultColumnOptions{
		Profile: profile, DatabaseTypes: databaseTypes,
	})
}
