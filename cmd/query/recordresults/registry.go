// Package recordresults serves record streams through the profile engine: one
// read-only `sql` profile per result type, over the sqlite index a
// recordstore.Indexer keeps caught up, addressed by a stream param.
//
// One profile per type rather than per stream is the point. Every profile is a
// catalog entry, an OpenAPI path and a sidebar item, so a profile per stream
// would grow all three with every capture; a stream param grows none of them.
// Paging, column filters, filter value lookups, sort and export are the
// engine's own, applied to the index by SQL.
package recordresults

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// MaxExportRows is where an all-row export of a result stops. It is well above
// the engine default because a capture is read whole far more often than a
// table is: a trace of a busy run is tens of thousands of rows.
const MaxExportRows = 1_000_000

const (
	streamParam = "stream"
	fromParam   = "from"
	toParam     = "to"
	seqColumn   = "seq"
	streamIDKey = "stream_id"
)

var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// RegistryOptions configure a Registry.
type RegistryOptions struct {
	// Prefix names every result profile <prefix>/<kind>, and namespaces the
	// index connection.
	Prefix string

	// Schemas is the kind catalog Index resolves kinds through.
	// RegisterResultType fills it.
	Schemas *recordstore.Schemas

	// Index is the sqlite file result profiles read. It must have been opened
	// with Schemas.Columns as its schema.
	Index *sqlite.Backend

	// Source is the authoritative backend result streams are written to. The
	// registry constructs the Indexer that mirrors it into Index, so preparation
	// and profile reads cannot accidentally name different indexes.
	Source recordstore.Backend

	// ConnectionName names the index's virtual connection,
	// connection://<prefix>/<name>.
	ConnectionName string
}

// ResultType declares one kind of record a stream holds, and the profile that
// serves it.
type ResultType[T any] struct {
	// Kind is the stream kind, and the last segment of the profile name.
	Kind string

	// Title is the human name of the result type.
	Title string

	// TimeColumn, when set, names T's datetime column: it becomes the table's
	// timestamp and the rows are ordered newest first. Without it rows are in
	// seq order.
	TimeColumn string
}

// RegisteredResultType is one registered result type as a caller finds it.
type RegisteredResultType struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Profile string `json:"profile"`
}

// Registry is the result types a server serves: a profiles.VirtualStore of
// their profiles, the resolver of the index connection those profiles read,
// and the BeforeExecute hook that catches the index up first.
type Registry struct {
	prefix     string
	schemas    *recordstore.Schemas
	index      *sqlite.Backend
	indexer    *recordstore.Indexer
	connection models.Connection

	mu      sync.RWMutex
	results map[string]registeredResult
}

type registeredResult struct {
	RegisteredResultType
	profile query.Profile
}

// NewRegistry validates options and returns an empty registry.
func NewRegistry(options RegistryOptions) (*Registry, error) {
	switch {
	case !segmentPattern.MatchString(options.Prefix):
		return nil, fmt.Errorf("result registry prefix %q must be one path segment of letters, digits, . _ -", options.Prefix)
	case options.Schemas == nil:
		return nil, fmt.Errorf("result registry: kind schemas are required")
	case options.Index == nil:
		return nil, fmt.Errorf("result registry: a sqlite index is required")
	case options.Source == nil:
		return nil, fmt.Errorf("result registry: a record source is required")
	case !segmentPattern.MatchString(options.ConnectionName):
		return nil, fmt.Errorf("result registry connection name %q must be one segment of letters, digits, . _ -", options.ConnectionName)
	}
	indexer, err := recordstore.NewIndexer(options.Source, options.Index)
	if err != nil {
		return nil, fmt.Errorf("result registry: %w", err)
	}
	now := time.Now()
	return &Registry{
		prefix: options.Prefix, schemas: options.Schemas, index: options.Index, indexer: indexer,
		connection: models.Connection{
			ID: uuid.New(), Name: options.ConnectionName, Namespace: options.Prefix, Source: "recordstore",
			Type: models.ConnectionTypeSQLite, URL: options.Index.ReadDSN(), Virtual: true, ReadOnly: true,
			CreatedAt: now, UpdatedAt: now,
		},
		results: map[string]registeredResult{},
	}, nil
}

// RegisterResultType declares T's kind: its columns (reflected through
// query.ColumnsFor) become the kind's schema and index table, and a profile
// <prefix>/<kind> serves its streams.
func RegisterResultType[T any](registry *Registry, resultType ResultType[T]) error {
	if resultType.Title == "" {
		return fmt.Errorf("result type %q needs a title", resultType.Kind)
	}
	if err := recordstore.ValidateKind(resultType.Kind); err != nil {
		return err
	}
	columns, err := query.ColumnsFor(reflect.TypeFor[T]())
	if err != nil {
		return fmt.Errorf("result type %q: %w", resultType.Kind, err)
	}
	if err := markTimeColumn(columns, resultType.TimeColumn); err != nil {
		return fmt.Errorf("result type %q: %w", resultType.Kind, err)
	}
	for _, column := range columns {
		if column.Name == seqColumn || column.Name == streamIDKey {
			return fmt.Errorf("result type %q declares %q, which every stream table reserves", resultType.Kind, column.Name)
		}
	}
	presenter, err := newTypedRowPresenter[T]()
	if err != nil {
		return fmt.Errorf("result type %q: %w", resultType.Kind, err)
	}
	return registry.register(RegisteredResultType{
		Kind: resultType.Kind, Title: resultType.Title, Profile: registry.prefix + "/" + resultType.Kind,
	}, columns, resultType.TimeColumn, presenter)
}

type typedRowPresenter[T any] struct {
	columns []api.ColumnDef
}

func newTypedRowPresenter[T any]() (query.RowPresenter, error) {
	typeOfT := reflect.TypeFor[T]()
	tableProviderType := reflect.TypeFor[api.TableProvider]()
	if typeOfT.Kind() == reflect.Interface {
		return nil, nil
	}
	var value reflect.Value
	switch {
	case typeOfT.Implements(tableProviderType):
		if typeOfT.Kind() == reflect.Pointer {
			value = reflect.New(typeOfT.Elem())
		} else {
			value = reflect.New(typeOfT).Elem()
		}
	case reflect.PointerTo(typeOfT).Implements(tableProviderType):
		value = reflect.New(typeOfT)
	default:
		return nil, nil
	}
	provider, ok := value.Interface().(api.TableProvider)
	if !ok {
		return nil, fmt.Errorf("%s declares TableProvider but cannot be instantiated", typeOfT)
	}
	columns := append([]api.ColumnDef{api.Column(seqColumn).Label("Seq").Build()}, provider.Columns()...)
	return typedRowPresenter[T]{columns: columns}, nil
}

func (p typedRowPresenter[T]) Columns() []api.ColumnDef { return slices.Clone(p.columns) }

func (typedRowPresenter[T]) Present(row query.Row) (map[string]any, error) {
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("encode indexed row: %w", err)
	}
	var value T
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, fmt.Errorf("decode indexed row as %s: %w", reflect.TypeFor[T](), err)
	}
	provider, ok := any(value).(api.TableProvider)
	if !ok {
		provider, ok = any(&value).(api.TableProvider)
	}
	if !ok {
		return nil, fmt.Errorf("decoded %s does not implement TableProvider", reflect.TypeFor[T]())
	}
	presented := provider.Row()
	if presented == nil {
		return nil, fmt.Errorf("%s TableProvider returned a nil row", reflect.TypeFor[T]())
	}
	presented[seqColumn] = row[seqColumn]
	return presented, nil
}

func markTimeColumn(columns []query.ColumnDef, name string) error {
	if name == "" {
		return nil
	}
	index := slices.IndexFunc(columns, func(column query.ColumnDef) bool { return column.Name == name })
	if index < 0 {
		return fmt.Errorf("time column %q is not one of its columns", name)
	}
	if columns[index].Type != query.ColumnTypeDateTime {
		return fmt.Errorf("time column %q is %s, not a datetime", name, columns[index].Type)
	}
	columns[index].Kind = query.ColumnKindTimestamp
	return nil
}

func (r *Registry) register(result RegisteredResultType, columns []query.ColumnDef, timeColumn string, presenter query.RowPresenter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.results[result.Profile]; exists {
		return fmt.Errorf("result type %q is already registered", result.Kind)
	}
	if err := r.schemas.Register(result.Kind, columns); err != nil {
		return err
	}
	table, err := r.index.Table(result.Kind)
	if err != nil {
		return err
	}
	profile, err := r.resultProfile(result.Profile, table, columns, timeColumn)
	if err != nil {
		return fmt.Errorf("result type %q: %w", result.Kind, err)
	}
	profile.Presenter = presenter
	r.results[result.Profile] = registeredResult{RegisteredResultType: result, profile: profile}
	return nil
}

// resultProfile is the profile over one kind's index table. Every edge of the
// window binds as a placeholder, and seq breaks every tie, which is what lets
// the engine page it past the first page.
func (r *Registry) resultProfile(name string, table sqlitetable.Table, columns []query.ColumnDef, timeColumn string) (query.Profile, error) {
	stream, err := table.Physical(streamIDKey)
	if err != nil {
		return query.Profile{}, err
	}
	seq, err := table.Physical(seqColumn)
	if err != nil {
		return query.Profile{}, err
	}
	order := query.Order{{Column: seqColumn, Unique: true}}
	if timeColumn != "" {
		order = append(query.Order{{Column: timeColumn, Desc: true}}, order...)
	}
	profileColumns := append([]query.ColumnDef{{Name: seqColumn, Label: "Seq", Type: query.ColumnTypeNumber}}, columns...)
	profileColumns = append(profileColumns, query.ColumnDef{Name: streamIDKey, Type: query.ColumnTypeString, Hidden: true})
	profile := query.Profile{
		Name: name, Virtual: true, ReadOnly: true,
		Provider: query.ProviderConfig{Type: "sqlite", Connection: r.connectionReference()},
		Query: table.Select() + fmt.Sprintf(` WHERE %s = {{.params.%s}} AND %s > {{.params.%s}} AND %s <= {{.params.%s}}`,
			stream, streamParam, seq, fromParam, seq, toParam),
		Params:  resultParams(),
		Columns: sqlitetable.ProfileColumns(profileColumns),
		Order:   order,
		Limits:  &query.RowLimits{PageSize: 100, MaxPageSize: 500, MaxExportRows: MaxExportRows},
		Output:  []string{"table", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf"},
	}
	if _, err := profile.FilterBindings(); err != nil {
		return query.Profile{}, err
	}
	if err := profile.Pageable(); err != nil {
		return query.Profile{}, err
	}
	return profile, nil
}

// resultParams address a stream and, optionally, a seq window of it. The
// window defaults to the whole stream: after seq 0, through the largest seq
// there can be.
func resultParams() []query.ParamDef {
	return []query.ParamDef{
		{Name: streamParam, Label: "Stream", Required: true, Description: "The record stream to read"},
		{Name: fromParam, Label: "After seq", Type: query.ParamTypeNumber, Default: int64(0), Description: "Read the rows after this seq"},
		{Name: toParam, Label: "Through seq", Type: query.ParamTypeNumber, Default: int64(math.MaxInt64), Description: "Read the rows up to and including this seq"},
	}
}

func (r *Registry) connectionReference() string {
	return "connection://" + r.connection.Namespace + "/" + r.connection.Name
}

// ResultTypes lists the registered result types by kind.
func (r *Registry) ResultTypes() []RegisteredResultType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]RegisteredResultType, 0, len(r.results))
	for _, result := range r.results {
		types = append(types, result.RegisteredResultType)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Kind < types[j].Kind })
	return types
}
