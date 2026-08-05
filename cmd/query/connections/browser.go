package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	clickycache "github.com/flanksource/clicky/cache"
	clickyvalkey "github.com/flanksource/clicky/valkey"
	"github.com/valkey-io/valkey-go"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	queryschema "github.com/flanksource/commons-db/query/schema"
)

type connectionBrowserHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

func newConnectionBrowserHandler(prefix string, ctx dbcontext.Context, next http.Handler) *connectionBrowserHandler {
	return &connectionBrowserHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next}
}

type browserDescriptor struct {
	Kind           string             `json:"kind"`
	Provider       string             `json:"provider,omitempty"`
	Language       string             `json:"language,omitempty"`
	QueryLabel     string             `json:"queryLabel,omitempty"`
	DefaultQuery   string             `json:"defaultQuery,omitempty"`
	ResultView     string             `json:"resultView,omitempty"`
	OptionsSchema  queryschema.Schema `json:"optionsSchema,omitempty"`
	InitialOptions map[string]any     `json:"initialOptions,omitempty"`
	Catalog        bool               `json:"catalog,omitempty"`
	// TargetLabel names what a query runs against when the source picks one flat
	// target — the `index` option — instead of navigating a hierarchy. Setting it
	// gives the browser a target combobox in place of the catalog tree.
	TargetLabel string `json:"targetLabel,omitempty"`
	// RowLimits are the row caps that apply when a profile sets none of its own:
	// the page it returns by default, the largest page a caller may ask for, and
	// where an export stops. The browser shows them beside the query's own limit
	// — an option the author does set — so the four are not confused.
	RowLimits *browserRowLimits `json:"rowLimits,omitempty"`
}

// browserRowLimits carries the default row caps to the browser, which shows
// them as the values a profile inherits when it declares no limits of its own.
type browserRowLimits struct {
	PageSize      int `json:"pageSize"`
	MaxPageSize   int `json:"maxPageSize"`
	MaxExportRows int `json:"maxExportRows"`
}

type browserQueryRequest struct {
	Query   string         `json:"query"`
	Options map[string]any `json:"options,omitempty"`

	// Columns is the column set a previous run returned, echoed back verbatim so
	// a filter binds to the field and kind the console offered rather than to
	// whatever a filtered result happens to describe. A source with a catalog of
	// its own ignores it and reads that instead.
	Columns []browserColumn `json:"columns,omitempty"`

	// Filters is filter.<column> → comma-joined values, "!" to exclude — the
	// same encoding a stored profile's filter bar sends, so one console reads
	// both surfaces.
	Filters map[string]string `json:"filters,omitempty"`
}

type browserQueryResult struct {
	Rows         []query.Row     `json:"rows,omitempty"`
	Columns      []browserColumn `json:"columns,omitempty"`
	AffectedRows *int64          `json:"affectedRows,omitempty"`
	DurationMS   float64         `json:"durationMs"`
	Message      string          `json:"message,omitempty"`

	// Truncated reports that the console stopped short of the whole result, and
	// Limit is where it stopped. Both are typed fields rather than Metadata
	// entries because the browser reads them to render the bound — a cap buried
	// in an open map is a cap the console never shows, which is the same as not
	// having one.
	Truncated bool `json:"truncated,omitempty"`
	Limit     int  `json:"limit,omitempty"`

	Metadata map[string]any `json:"metadata,omitempty"`
}

type browserColumn struct {
	Name         string `json:"name"`
	DatabaseType string `json:"databaseType,omitempty"`

	// FilterKey is the parameter this column narrows on. Empty means the source
	// cannot narrow on it — a computed expression, or a type with no comparison.
	FilterKey string               `json:"filterKey,omitempty"`
	Filter    *browserColumnFilter `json:"filter,omitempty"`
}

type browserInspection struct {
	Kind           string                          `json:"kind"`
	Dialect        string                          `json:"dialect,omitempty"`
	Database       string                          `json:"database,omitempty"`
	Databases      []string                        `json:"databases,omitempty"`
	DefaultSchema  string                          `json:"defaultSchema,omitempty"`
	Nodes          []browserCatalogNode            `json:"nodes,omitempty"`
	Schemas        []sqlinspect.Schema             `json:"schemas,omitempty"`
	Targets        []opensearchinspect.Target      `json:"targets,omitempty"`
	Selected       *opensearchinspect.FieldCatalog `json:"selected,omitempty"`
	Truncated      bool                            `json:"truncated,omitempty"`
	TruncateReason string                          `json:"truncateReason,omitempty"`
}

func (h *connectionBrowserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := h.prefix + "/connection/"
	if !strings.HasPrefix(r.URL.Path, base) {
		h.next.ServeHTTP(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, base)
	idPart, resource, ok := strings.Cut(rest, "/")
	if !ok || idPart == "" {
		h.next.ServeHTTP(w, r)
		return
	}
	id, err := url.PathUnescape(strings.Trim(idPart, "/"))
	if err != nil {
		http.Error(w, "invalid connection id", http.StatusBadRequest)
		return
	}
	if strings.TrimSuffix(resource, "/") == "info" && r.Method == http.MethodGet {
		h.serveConnectionInfo(w, r, id)
		return
	}
	if resource != "browser" && !strings.HasPrefix(resource, "browser/") {
		h.next.ServeHTTP(w, r)
		return
	}
	tail := strings.TrimPrefix(resource, "browser")
	conn, err := findConnectionMust(h.ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if conn.Type == models.ConnectionTypeOpenTelemetry {
		openTelemetry, resolveErr := dbconnection.NewOpenTelemetry(conn)
		if resolveErr != nil {
			http.Error(w, resolveErr.Error(), http.StatusUnprocessableEntity)
			return
		}
		conn, resolveErr = openTelemetry.ResolveOpenSearch(h.ctx)
		if resolveErr != nil {
			http.Error(w, resolveErr.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	tail = strings.TrimSuffix(tail, "/")

	switch {
	case tail == "" && r.Method == http.MethodGet:
		descriptor, ok := descriptorForConnection(conn.Type)
		if !ok {
			http.Error(w, fmt.Sprintf("connection type %q has no browser", conn.Type), http.StatusNotFound)
			return
		}
		writeJSON(w, descriptor)
	case tail == "/query" && r.Method == http.MethodPost:
		h.serveQuery(w, r, conn)
	case tail == "/compile" && r.Method == http.MethodPost:
		h.serveCompile(w, r, conn)
	case tail == "/values" && r.Method == http.MethodPost:
		h.serveValues(w, r, conn)
	case tail == "/filters/values" && r.Method == http.MethodPost:
		h.serveFilterValues(w, r, conn)
	case tail == "/catalog" && r.Method == http.MethodGet:
		h.serveCatalog(w, r, conn)
	case tail == "/inspect" && r.Method == http.MethodGet:
		h.serveInspection(w, r, conn)
	case strings.HasPrefix(tail, "/cache/"):
		h.serveCache(w, r, conn, h.prefix+"/connection/"+idPart+"/browser")
	default:
		h.next.ServeHTTP(w, r)
	}
}

func findConnectionMust(ctx dbcontext.Context, id string) (*models.Connection, error) {
	conn, err := findConnection(ctx.DB(), id)
	if err != nil {
		return nil, fmt.Errorf("connection %q not found: %w", id, err)
	}
	hydrated, err := dbcontext.HydrateConnection(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("hydrate connection %q: %w", id, err)
	}
	return hydrated, nil
}

func descriptorForConnection(connType string) (browserDescriptor, bool) {
	d := browserDescriptor{Kind: "query", ResultView: "table"}
	switch connType {
	case models.ConnectionTypePostgres:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "postgres", "sql", "SQL", "SELECT 1", true
	case models.ConnectionTypeMySQL:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "mysql", "sql", "SQL", "SELECT 1", true
	case models.ConnectionTypeSQLServer:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "sqlserver", "sql", "SQL", "SELECT TOP 100 * FROM INFORMATION_SCHEMA.TABLES", true
	case models.ConnectionTypeClickHouse:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "clickhouse", "sql", "SQL", "SELECT 1", true
	case models.ConnectionTypeHTTP:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery = "http", "text", "Relative request path", "/"
		d.InitialOptions = map[string]any{"method": http.MethodGet}
	case models.ConnectionTypePrometheus:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.ResultView = "prometheus", "text", "PromQL", "up", "timeseries"
		d.InitialOptions = map[string]any{"range": map[string]any{"start": "now-1h", "end": "now", "step": "30s"}}
	case models.ConnectionTypeLoki:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.ResultView = "loki", "text", "LogQL", `{job=~".+"}`, "logs"
		d.InitialOptions = map[string]any{"since": "1h", "limit": "200", "direction": "backward"}
	case models.ConnectionTypeOpenSearch:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "opensearch", "json", "OpenSearch query DSL", `{"query":{"match_all":{}}}`, true
		d.TargetLabel = "Index"
		d.InitialOptions = map[string]any{"limit": "200"}
	case models.ConnectionTypeJaeger:
		d.Provider, d.Language, d.QueryLabel, d.ResultView = "jaeger", "text", "Trace ID (optional)", "table"
		d.InitialOptions = map[string]any{"lookback": "1h", "limit": "20"}
	case models.ConnectionTypeRedis:
		return browserDescriptor{Kind: "cache"}, true
	default:
		return browserDescriptor{}, false
	}
	d.OptionsSchema = queryschema.BrowserOptions(d.Provider)
	defaults := (*query.RowLimits)(nil).Resolve()
	d.RowLimits = &browserRowLimits{
		PageSize:      defaults.PageSize,
		MaxPageSize:   defaults.MaxPageSize,
		MaxExportRows: defaults.MaxExportRows,
	}
	return d, true
}

func (h *connectionBrowserHandler) serveQuery(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	descriptor, ok := descriptorForConnection(conn.Type)
	if !ok || descriptor.Kind != "query" {
		http.Error(w, "connection does not support queries", http.StatusBadRequest)
		return
	}
	var request browserQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "decode browser query: "+err.Error(), http.StatusBadRequest)
		return
	}
	// A structured OpenSearch search stands in for the query text: the builder
	// sends the specification and lets the server compile it.
	structured := descriptor.Provider == "opensearch" && request.Options["search"] != nil
	if strings.TrimSpace(request.Query) == "" && descriptor.Provider != "jaeger" && !structured {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	for _, key := range []string{"url", "address", "type"} {
		delete(request.Options, key)
	}
	if descriptor.Provider == "http" {
		parsed, err := url.Parse(request.Query)
		if err != nil || parsed.IsAbs() || parsed.Host != "" {
			http.Error(w, "HTTP browser queries must be relative to the selected connection", http.StatusBadRequest)
			return
		}
	}

	started := time.Now()
	var result browserQueryResult
	var err error
	switch descriptor.Provider {
	case "postgres", "mysql", "sqlserver", "clickhouse":
		database, _ := request.Options["database"].(string)
		result, err = h.executeSQL(r, conn, descriptor, request, database)
	case "opensearch":
		result, err = h.executeOpenSearch(r, conn, descriptor, request)
	default:
		var provider query.Provider
		provider, err = query.GetProvider(descriptor.Provider)
		if err == nil {
			result.Rows, err = provider.Execute(h.ctx, query.ProviderRequest{
				Connection: conn.ID.String(), Query: request.Query, Options: request.Options,
			})
		}
	}
	result.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, result)
}

func (h *connectionBrowserHandler) executeSQL(
	r *http.Request,
	conn *models.Connection,
	descriptor browserDescriptor,
	request browserQueryRequest,
	database string,
) (browserQueryResult, error) {
	statement := request.Query
	client, err := h.sqlClient(r.Context(), conn, database)
	if err != nil {
		return browserQueryResult{}, err
	}
	defer client.Close()

	// A selection narrows the statement's result, so it is applied by wrapping
	// the statement rather than by editing it — the same wrapper a stored
	// profile's filters go through, so the console and a profile cannot
	// disagree about what a filter means.
	profile := browserProfile(descriptor, conn, statement, request.Options, browserColumnDefs(request.Columns))
	filters, err := resolveBrowserFilters(profile, request.Filters)
	if err != nil {
		return browserQueryResult{}, err
	}
	var args []any
	if len(filters) > 0 {
		statement, args, err = providers.FilterSQL(conn.Type, statement, filters)
		if err != nil {
			return browserQueryResult{}, err
		}
	}

	if !sqlReturnsRows(statement) {
		// The connection browser intentionally accepts a complete operator-authored
		// statement; no request value is interpolated into another SQL command.
		// codeql[go/sql-injection]
		res, err := client.ExecContext(r.Context(), statement)
		if err != nil {
			return browserQueryResult{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return browserQueryResult{Message: "Statement executed successfully"}, nil
		}
		return browserQueryResult{AffectedRows: &affected, Message: "Statement executed successfully"}, nil
	}

	// The connection browser intentionally accepts a complete operator-authored
	// statement; no request value is interpolated into another SQL query. When a
	// filter is active the statement is that same query wrapped in a CTE built
	// from constant syntax, and every filter value travels as a bound arg.
	// codeql[go/sql-injection]
	rows, err := client.QueryContext(r.Context(), statement, args...)
	if err != nil {
		return browserQueryResult{}, err
	}
	defer rows.Close()
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return browserQueryResult{}, err
	}
	databaseTypes := make(map[string]string, len(columnTypes))
	for _, column := range columnTypes {
		if name := sqlColumnTypeName(column.DatabaseTypeName()); name != "" {
			databaseTypes[column.Name()] = name
		}
	}
	// The result's own columns describe it, so a filtered run keeps offering the
	// filters that produced it.
	described := browserProfile(descriptor, conn, request.Query, request.Options, sqlBrowserColumns(columnTypes))
	columns, err := describeBrowserColumns(described, databaseTypes)
	if err != nil {
		return browserQueryResult{}, err
	}
	// The console is interactive, so it reads a page rather than a table. One
	// row past the page is read and discarded to tell "this is all of it" from
	// "this is the start of it" — a console that silently shows a slice cannot
	// be used to decide anything.
	ceiling := query.DefaultPageSize
	values := make([]query.Row, 0, ceiling)
	scanner, err := db.NewRowScanner(rows)
	if err != nil {
		return browserQueryResult{}, err
	}
	for len(values) < ceiling && scanner.Next() {
		values = append(values, query.Row(scanner.Row()))
	}
	if err := scanner.Err(); err != nil {
		return browserQueryResult{}, err
	}
	truncated := scanner.Next()
	if err := scanner.Err(); err != nil {
		return browserQueryResult{}, err
	}
	return browserQueryResult{
		Rows: values, Columns: columns, Truncated: truncated, Limit: ceiling,
	}, nil
}

func sqlReturnsRows(statement string) bool {
	statement = strings.ToLower(strings.TrimSpace(statement))
	for _, prefix := range []string{"select", "with", "show", "describe", "desc", "explain", "pragma", "values"} {
		if strings.HasPrefix(statement, prefix) {
			return true
		}
	}
	return false
}

func (h *connectionBrowserHandler) serveInspection(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	inspection, err := h.inspectConnection(ctx, conn, r.URL.Query().Get("database"), r.URL.Query().Get("target"), r.URL.Query().Get("targetKind"))
	if err != nil {
		http.Error(w, sanitizeConnectionError(err, conn), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, inspection)
}

func (h *connectionBrowserHandler) inspectConnection(ctx context.Context, conn *models.Connection, database, targetName, targetKind string) (browserInspection, error) {
	switch conn.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse:
		catalog, err := h.inspectSQL(ctx, conn, database)
		if err != nil {
			return browserInspection{}, err
		}
		return browserInspection{
			Kind: "sql", Dialect: sqlDialect(conn.Type), Database: catalog.Database, Databases: catalog.Databases,
			DefaultSchema: catalog.DefaultSchema, Schemas: catalog.Schemas, Nodes: catalogNodesForSQL(conn.Type, catalog),
			Truncated: catalog.Truncated, TruncateReason: catalog.TruncateReason,
		}, nil
	case models.ConnectionTypeOpenSearch:
		requestCtx := h.ctx.Wrap(ctx)
		searcher, err := h.openSearchSearcher(requestCtx, conn)
		if err != nil {
			return browserInspection{}, err
		}
		inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{})
		if err != nil {
			return browserInspection{}, err
		}
		targets, err := inspector.Targets(ctx)
		if err != nil {
			return browserInspection{}, err
		}
		inspection := browserInspection{Kind: "opensearch", Targets: targets.Targets, Nodes: catalogNodesForOpenSearch(targets.Targets), Truncated: targets.Truncated, TruncateReason: targets.TruncateReason}
		if targetName == "" {
			return inspection, nil
		}
		var selected *opensearchinspect.Target
		for i := range targets.Targets {
			if targets.Targets[i].Name == targetName && targets.Targets[i].Kind == targetKind {
				selected = &targets.Targets[i]
				break
			}
		}
		// A wildcard is a target by construction — `_field_caps` resolves it — so
		// an author can type one the enumeration never listed, which matters when
		// the target list was truncated. A concrete name still has to exist.
		if selected == nil && strings.Contains(targetName, "*") {
			selected = &opensearchinspect.Target{Name: targetName, Kind: "pattern"}
		}
		if selected == nil {
			return browserInspection{}, fmt.Errorf("OpenSearch target %q (%s) was not discovered", targetName, targetKind)
		}
		fields, err := inspector.Fields(ctx, *selected)
		if err != nil {
			return browserInspection{}, err
		}
		inspection.Selected = &fields
		return inspection, nil
	default:
		return browserInspection{}, fmt.Errorf("connection type %q does not support inspection", conn.Type)
	}
}

func (h *connectionBrowserHandler) inspectSQL(ctx context.Context, conn *models.Connection, database string) (sqlinspect.Catalog, error) {
	client, err := h.sqlClient(ctx, conn, database)
	if err != nil {
		return sqlinspect.Catalog{}, err
	}
	defer client.Close()
	return sqlinspect.Inspect(ctx, client, conn.Type, sqlinspect.Limits{})
}

func (h *connectionBrowserHandler) sqlClient(ctx context.Context, conn *models.Connection, database string) (*sql.DB, error) {
	var sqlConn dbconnection.SQLConnection
	if err := sqlConn.FromModel(*conn); err != nil {
		return nil, err
	}
	client, err := sqlConn.Client(h.ctx.Wrap(ctx))
	if err != nil {
		return nil, err
	}
	database = strings.TrimSpace(database)
	if database == "" {
		return client, nil
	}
	databases, err := sqlinspect.ListDatabases(ctx, client, conn.Type)
	if err != nil {
		client.Close()
		return nil, err
	}
	if !slices.Contains(databases, database) {
		client.Close()
		return nil, fmt.Errorf("SQL database %q was not discovered", database)
	}
	client.Close()
	sqlConn, err = sqlConn.UseDatabase(database)
	if err != nil {
		return nil, err
	}
	return sqlConn.Client(h.ctx.Wrap(ctx))
}

func sqlDialect(connType string) string {
	switch connType {
	case models.ConnectionTypePostgres:
		return "postgresql"
	case models.ConnectionTypeMySQL:
		return "mysql"
	case models.ConnectionTypeSQLServer:
		return "mssql"
	default:
		return "standard"
	}
}

func (h *connectionBrowserHandler) serveCache(w http.ResponseWriter, r *http.Request, conn *models.Connection, prefix string) {
	if conn.Type != models.ConnectionTypeRedis {
		http.Error(w, "connection is not Redis/Valkey", http.StatusBadRequest)
		return
	}
	option, err := valkey.ParseURL(conn.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if conn.Username != "" {
		option.Username = conn.Username
	}
	if conn.Password != "" {
		option.Password = conn.Password
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	defer client.Close()
	browser := clickyvalkey.NewBrowser(client, clickyvalkey.BrowserConfig{})
	clickycache.Handler(browser, prefix).ServeHTTP(w, r)
}
