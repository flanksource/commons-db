package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	clickycache "github.com/flanksource/clicky/cache"
	clickyvalkey "github.com/flanksource/clicky/valkey"
	"github.com/valkey-io/valkey-go"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/flanksource/commons-db/types"
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

type browserCatalog struct {
	Nodes []browserCatalogNode `json:"nodes"`
}

type browserCatalogNode struct {
	ID       string               `json:"id"`
	Label    string               `json:"label"`
	Kind     string               `json:"kind"`
	Query    string               `json:"query,omitempty"`
	Options  map[string]any       `json:"options,omitempty"`
	Children []browserCatalogNode `json:"children,omitempty"`
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
		writeK8sJSON(w, descriptor)
	case tail == "/query" && r.Method == http.MethodPost:
		h.serveQuery(w, r, conn)
	case tail == "/catalog" && r.Method == http.MethodGet:
		h.serveCatalog(w, r, conn)
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
		result, err = h.executeSQL(r, conn, request.Query)
	case "opensearch":
		result, err = h.executeOpenSearch(conn, request)
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
	writeK8sJSON(w, result)
}

func (h *connectionBrowserHandler) executeSQL(r *http.Request, conn *models.Connection, statement string) (browserQueryResult, error) {
	var sqlConn dbconnection.SQLConnection
	if err := sqlConn.FromModel(*conn); err != nil {
		return browserQueryResult{}, err
	}
	client, err := sqlConn.Client(h.ctx)
	if err != nil {
		return browserQueryResult{}, err
	}
	defer client.Close()

	if !sqlReturnsRows(statement) {
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

func (h *connectionBrowserHandler) executeOpenSearch(conn *models.Connection, request browserQueryRequest) (browserQueryResult, error) {
	index, _ := request.Options["index"].(string)
	limit := ""
	if value := request.Options["limit"]; value != nil {
		limit = fmt.Sprint(value)
	}
	if index == "" {
		return browserQueryResult{}, fmt.Errorf("OpenSearch index is required")
	}
	backend := opensearch.Backend{Address: conn.URL}
	if conn.Username != "" {
		backend.Username = &types.EnvVar{ValueStatic: conn.Username}
	}
	if conn.Password != "" {
		backend.Password = &types.EnvVar{ValueStatic: conn.Password}
	}
	searcher, err := opensearch.New(h.ctx, backend, nil)
	if err != nil {
		return browserQueryResult{}, err
	}
	raw, err := searcher.SearchRaw(h.ctx, opensearch.Request{Index: index, Query: request.Query, Limit: limit})
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

func (h *connectionBrowserHandler) serveCatalog(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	switch conn.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse:
		catalog, err := h.sqlCatalog(r, conn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeK8sJSON(w, catalog)
	case models.ConnectionTypeOpenSearch:
		catalog, err := h.openSearchCatalog(r, conn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeK8sJSON(w, catalog)
	default:
		writeK8sJSON(w, browserCatalog{Nodes: []browserCatalogNode{}})
	}
}

func (h *connectionBrowserHandler) sqlCatalog(r *http.Request, conn *models.Connection) (browserCatalog, error) {
	var sqlConn dbconnection.SQLConnection
	if err := sqlConn.FromModel(*conn); err != nil {
		return browserCatalog{}, err
	}
	client, err := sqlConn.Client(h.ctx)
	if err != nil {
		return browserCatalog{}, err
	}
	defer client.Close()
	statement := `SELECT table_schema, table_name, column_name, data_type FROM information_schema.columns WHERE table_schema NOT IN ('information_schema','pg_catalog','sys') ORDER BY table_schema, table_name, ordinal_position`
	if conn.Type == models.ConnectionTypeClickHouse {
		statement = `SELECT database AS table_schema, table AS table_name, name AS column_name, type AS data_type FROM system.columns WHERE database NOT IN ('system','information_schema') ORDER BY database, table, position`
	}
	rows, err := client.QueryContext(r.Context(), statement)
	if err != nil {
		return browserCatalog{}, err
	}
	defer rows.Close()
	type column struct{ schema, table, name, dataType string }
	var columns []column
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.schema, &item.table, &item.name, &item.dataType); err != nil {
			return browserCatalog{}, err
		}
		columns = append(columns, item)
	}
	if err := rows.Err(); err != nil {
		return browserCatalog{}, err
	}
	groups := map[string]map[string][]browserCatalogNode{}
	for _, item := range columns {
		if groups[item.schema] == nil {
			groups[item.schema] = map[string][]browserCatalogNode{}
		}
		groups[item.schema][item.table] = append(groups[item.schema][item.table], browserCatalogNode{
			ID: item.schema + "." + item.table + "." + item.name, Label: item.name + " · " + item.dataType, Kind: "column",
		})
	}
	schemas := make([]string, 0, len(groups))
	for name := range groups {
		schemas = append(schemas, name)
	}
	sort.Strings(schemas)
	catalog := browserCatalog{}
	for _, schemaName := range schemas {
		tables := make([]string, 0, len(groups[schemaName]))
		for name := range groups[schemaName] {
			tables = append(tables, name)
		}
		sort.Strings(tables)
		schemaNode := browserCatalogNode{ID: schemaName, Label: schemaName, Kind: "schema"}
		for _, tableName := range tables {
			identifier := sqlIdentifier(conn.Type, schemaName, tableName)
			queryText := "SELECT * FROM " + identifier + " LIMIT 100"
			if conn.Type == models.ConnectionTypeSQLServer {
				queryText = "SELECT TOP 100 * FROM " + identifier
			}
			schemaNode.Children = append(schemaNode.Children, browserCatalogNode{
				ID: schemaName + "." + tableName, Label: tableName, Kind: "table", Query: queryText,
				Children: groups[schemaName][tableName],
			})
		}
		catalog.Nodes = append(catalog.Nodes, schemaNode)
	}
	return catalog, nil
}

func sqlIdentifier(connType, schema, table string) string {
	if connType == models.ConnectionTypeMySQL || connType == models.ConnectionTypeClickHouse {
		return "`" + strings.ReplaceAll(schema, "`", "``") + "`.`" + strings.ReplaceAll(table, "`", "``") + "`"
	}
	if connType == models.ConnectionTypeSQLServer {
		return "[" + strings.ReplaceAll(schema, "]", "]]") + "].[" + strings.ReplaceAll(table, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(schema, `"`, `""`) + `"."` + strings.ReplaceAll(table, `"`, `""`) + `"`
}

func (h *connectionBrowserHandler) openSearchCatalog(r *http.Request, conn *models.Connection) (browserCatalog, error) {
	httpConn := dbconnection.HTTPConnection{}
	if err := httpConn.FromModel(*conn); err != nil {
		return browserCatalog{}, err
	}
	client, err := dbconnection.CreateHTTPClient(h.ctx, httpConn)
	if err != nil {
		return browserCatalog{}, err
	}
	endpoint := strings.TrimRight(conn.URL, "/") + "/_cat/indices?format=json&h=index,health,status,docs.count,store.size"
	resp, err := client.R(r.Context()).Get(endpoint)
	if err != nil {
		return browserCatalog{}, err
	}
	defer resp.Body.Close()
	if !resp.IsOK() {
		return browserCatalog{}, fmt.Errorf("OpenSearch index catalog failed with status %d", resp.StatusCode)
	}
	var items []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return browserCatalog{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["index"] < items[j]["index"] })
	catalog := browserCatalog{}
	for _, item := range items {
		index := item["index"]
		catalog.Nodes = append(catalog.Nodes, browserCatalogNode{
			ID: index, Label: index, Kind: "index", Query: `{"query":{"match_all":{}}}`,
			Options: map[string]any{"index": index, "limit": "200"},
		})
	}
	return catalog, nil
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
