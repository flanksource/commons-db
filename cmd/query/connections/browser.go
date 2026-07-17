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
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
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
}

type browserQueryRequest struct {
	Query   string         `json:"query"`
	Options map[string]any `json:"options,omitempty"`
}

type browserQueryResult struct {
	Rows         []query.Row     `json:"rows,omitempty"`
	Columns      []browserColumn `json:"columns,omitempty"`
	AffectedRows *int64          `json:"affectedRows,omitempty"`
	DurationMS   float64         `json:"durationMs"`
	Message      string          `json:"message,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
}

type browserColumn struct {
	Name         string `json:"name"`
	DatabaseType string `json:"databaseType,omitempty"`
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
	if strings.TrimSpace(request.Query) == "" && descriptor.Provider != "jaeger" {
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
		result, err = h.executeSQL(r, conn, request.Query, database)
	case "opensearch":
		result, err = h.executeOpenSearch(r, conn, request)
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

func (h *connectionBrowserHandler) executeSQL(r *http.Request, conn *models.Connection, statement, database string) (browserQueryResult, error) {
	client, err := h.sqlClient(r.Context(), conn, database)
	if err != nil {
		return browserQueryResult{}, err
	}
	defer client.Close()

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
	// statement; no request value is interpolated into another SQL query.
	// codeql[go/sql-injection]
	rows, err := client.QueryContext(r.Context(), statement)
	if err != nil {
		return browserQueryResult{}, err
	}
	defer rows.Close()
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return browserQueryResult{}, err
	}
	columns := make([]browserColumn, 0, len(columnTypes))
	for _, column := range columnTypes {
		columns = append(columns, browserColumn{Name: column.Name(), DatabaseType: column.DatabaseTypeName()})
	}
	values, err := db.ScanRows[query.Row](rows)
	return browserQueryResult{Rows: values, Columns: columns}, err
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

func (h *connectionBrowserHandler) executeOpenSearch(r *http.Request, conn *models.Connection, request browserQueryRequest) (browserQueryResult, error) {
	index, _ := request.Options["index"].(string)
	limit := ""
	if value := request.Options["limit"]; value != nil {
		limit = fmt.Sprint(value)
	}
	if index == "" {
		return browserQueryResult{}, fmt.Errorf("OpenSearch index is required")
	}
	requestCtx := h.ctx.Wrap(r.Context())
	searcher, err := h.openSearchSearcher(requestCtx, conn)
	if err != nil {
		return browserQueryResult{}, err
	}
	raw, err := searcher.SearchRaw(requestCtx, opensearch.Request{Index: index, Query: request.Query, Limit: limit})
	if err != nil {
		return browserQueryResult{}, err
	}
	rows := make([]query.Row, 0, len(raw.Hits.Hits))
	for _, hit := range raw.Hits.Hits {
		row := query.Row{"_index": hit.Index, "_id": hit.ID, "_score": hit.Score}
		for key, value := range hit.Source {
			row[key] = value
		}
		rows = append(rows, row)
	}
	return browserQueryResult{Rows: rows, Metadata: map[string]any{
		"total": raw.Hits.Total.Value, "relation": raw.Hits.Total.Relation,
		"took": raw.Took, "timedOut": raw.TimedOut, "aggregations": raw.Aggregations,
	}}, nil
}

func (h *connectionBrowserHandler) openSearchSearcher(ctx dbcontext.Context, conn *models.Connection) (*opensearch.Searcher, error) {
	httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
	if err != nil {
		return nil, err
	}
	return opensearch.NewWithTransport(ctx, opensearch.Backend{Address: conn.URL}, nil, httpConnection.Transport())
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
