package connections

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	clickycache "github.com/flanksource/clicky/cache"
	clickyvalkey "github.com/flanksource/clicky/valkey"
	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"k8s.io/client-go/kubernetes"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	queryschema "github.com/flanksource/commons-db/query/schema"
)

type connectionBrowserHandler struct {
	prefix           string
	ctx              dbcontext.Context
	next             http.Handler
	sqlInspection    *inspection.Memo[sqlinspect.Catalog]
	kubernetesClient func(context.Context, *models.Connection) (kubernetes.Interface, error)
}

func newConnectionBrowserHandler(prefix string, ctx dbcontext.Context, next http.Handler) *connectionBrowserHandler {
	return &connectionBrowserHandler{
		prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next,
		sqlInspection: inspection.NewMemo(inspection.MemoOptions[sqlinspect.Catalog]{
			Policy: inspection.Policy(inspection.CacheClassSQLCatalog),
			Weight: sqlCatalogWeight,
		}),
		kubernetesClient: func(requestContext context.Context, connection *models.Connection) (kubernetes.Interface, error) {
			client, _, err := (dbconnection.KubeconfigConnection{
				ConnectionName: connection.ID.String(),
			}).Populate(ctx.Wrap(requestContext))
			if err != nil {
				return nil, fmt.Errorf("connect to Kubernetes connection %q: %w", connection.Name, err)
			}
			return client, nil
		},
	}
}

type browserTarget struct {
	Kind   string   `json:"kind"`
	Label  string   `json:"label"`
	Option string   `json:"option,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
}

// browserResultSort names the column a result is already ordered by and which
// way, mirroring the shape clicky-ui's DataTable takes as defaultSort.
type browserResultSort struct {
	Key string `json:"key"`
	Dir string `json:"dir"`
}

type browserDescriptor struct {
	Kind            string             `json:"kind"`
	Provider        string             `json:"provider,omitempty"`
	Language        string             `json:"language,omitempty"`
	QueryLabel      string             `json:"queryLabel,omitempty"`
	DefaultQuery    string             `json:"defaultQuery,omitempty"`
	ResultView      string             `json:"resultView,omitempty"`
	OptionsSchema   queryschema.Schema `json:"optionsSchema,omitempty"`
	InitialOptions  map[string]any     `json:"initialOptions,omitempty"`
	Catalog         bool               `json:"catalog,omitempty"`
	AllowEmptyQuery bool               `json:"allowEmptyQuery,omitempty"`
	Target          *browserTarget     `json:"target,omitempty"`
	// ResultSort is the order the provider actually returns rows in, so the
	// browser can render that order instead of imposing one of its own.
	//
	// It matters because a browser query is a bounded top-N, not the whole
	// result: re-sorting the 200 rows the server chose presents them as if the
	// cut had been made the other way round. Kubernetes is the case that proves
	// it — its API only resumes forward, so a limit returns the *oldest* lines,
	// and rendering those newest-first reads as "the latest logs".
	ResultSort *browserResultSort `json:"resultSort,omitempty"`
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
	Query             string                   `json:"query"`
	Options           map[string]any           `json:"options,omitempty"`
	Pagination        browserPaginationRequest `json:"pagination,omitempty"`
	RefreshInspection bool                     `json:"refreshInspection,omitempty"`

	// Columns is the column set a previous run returned, echoed back verbatim so
	// a filter binds to the field and kind the console offered rather than to
	// whatever a filtered result happens to describe. A source with a catalog of
	// its own ignores it and reads that instead.
	Columns []browserColumn `json:"columns,omitempty"`

	// Filters is filter.<column> → comma-joined values, "!" to exclude — the
	// same encoding a stored profile's filter bar sends, so one console reads
	// both surfaces.
	Filters map[string]string `json:"filters,omitempty"`

	diagnostics *query.ProviderDiagnostics
}

type browserPaginationRequest struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

func (r browserPaginationRequest) PageRequest() (query.PageRequest, error) {
	if r.Limit == 0 {
		r.Limit = query.DefaultPageSize
	}
	if r.Limit > query.DefaultMaxPageSize {
		return query.PageRequest{}, fmt.Errorf("page limit must be at most %d, got %d", query.DefaultMaxPageSize, r.Limit)
	}
	page := query.PageRequest{Limit: r.Limit, Offset: r.Offset, Strategy: query.PagingOffset}
	if err := page.Validate(); err != nil {
		return query.PageRequest{}, err
	}
	return page, nil
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

	Metadata    map[string]any             `json:"metadata,omitempty"`
	Pagination  *query.PageInfo            `json:"pagination,omitempty"`
	Diagnostics *query.ProviderDiagnostics `json:"diagnostics,omitempty"`
}

type browserColumn = query.ResultColumn

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
	Cache          *inspection.CacheMetadata       `json:"cache,omitempty"`
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
		status := http.StatusNotFound
		if errors.Is(err, dbcontext.ErrConnectionExpired) {
			status = http.StatusGone
		}
		http.Error(w, err.Error(), status)
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
	case tail == "/namespaces" && r.Method == http.MethodGet:
		h.serveKubernetesNamespaces(w, r, conn)
	case tail == "/workloads" && r.Method == http.MethodGet:
		h.serveKubernetesWorkloads(w, r, conn)
	case strings.HasPrefix(tail, "/cache/"):
		h.serveCache(w, r, conn, h.prefix+"/connection/"+idPart+"/browser")
	default:
		h.next.ServeHTTP(w, r)
	}
}

func findConnectionMust(ctx dbcontext.Context, id string) (*models.Connection, error) {
	reference := id
	if _, err := uuid.Parse(id); err != nil {
		reference = "connection://" + id
	}
	conn, err := dbcontext.HydrateConnectionByURL(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("connection %q not found: %w", id, err)
	}
	return conn, nil
}

// openSearchDirect reports whether a searcher is built straight from this
// connection. Elasticsearch is included because logs/opensearch speaks both
// wire protocols; OpenTelemetry is not, because it resolves a nested connection
// first — see inspectionOpenSearchSearcher.
func openSearchDirect(connType string) bool {
	return connType == models.ConnectionTypeOpenSearch || connType == models.ConnectionTypeElasticSearch
}

// openSearchBacked reports whether this connection is ultimately read through
// an OpenSearch searcher, and so has a catalog, a field mapping to inspect and
// a query DSL to compile.
func openSearchBacked(connType string) bool {
	return openSearchDirect(connType) || connType == models.ConnectionTypeOpenTelemetry
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
	case models.ConnectionTypeSQLite:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "sqlite", "sql", "SQL", "SELECT * FROM reconcile_rows LIMIT 100", true
	case models.ConnectionTypeHTTP:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery = "http", "text", "Relative request path", "/"
		d.InitialOptions = map[string]any{"method": http.MethodGet}
	case models.ConnectionTypePrometheus:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.ResultView = "prometheus", "text", "PromQL", "up", "timeseries"
		d.InitialOptions = map[string]any{"range": map[string]any{"start": "now-1h", "end": "now", "step": "30s"}}
	case models.ConnectionTypeLoki:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.ResultView = "loki", "text", "LogQL", `{job=~".+"}`, "logs"
		d.InitialOptions = map[string]any{"since": "1h", "limit": "200", "direction": "backward"}
		d.ResultSort = &browserResultSort{Key: "timestamp", Dir: "desc"} // direction=backward
	// Elasticsearch is browsed as OpenSearch: one searcher speaks both, so the
	// catalog, the index picker and the DSL editor are the same surface.
	case models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch:
		d.Provider, d.Language, d.QueryLabel, d.DefaultQuery, d.Catalog = "opensearch", "json", "OpenSearch query DSL", `{"query":{"match_all":{}}}`, true
		d.Target = &browserTarget{Kind: "index", Label: "Index", Option: "index"}
		d.InitialOptions = map[string]any{"limit": "200"}
	case models.ConnectionTypeOpenTelemetry:
		d.Provider, d.Language, d.QueryLabel, d.Catalog = "opentelemetry", "json", "OpenSearch query DSL", true
		d.Target = &browserTarget{Kind: "index", Label: "Index", Option: "index"}
		d.InitialOptions = map[string]any{"index": "otel-traces-*", "format": "flat", "limit": "200"}
	case models.ConnectionTypeJaeger:
		d.Provider, d.Language, d.QueryLabel, d.ResultView = "jaeger", "text", "Trace ID (optional)", "table"
		d.InitialOptions = map[string]any{"lookback": "1h", "limit": "20"}
	case models.ConnectionTypeAWS:
		d.Provider, d.Language, d.QueryLabel, d.ResultView = "cloudwatch", "text", "Logs Insights query", "logs"
		d.DefaultQuery = "fields @timestamp, @message | sort @timestamp desc | limit 100"
		d.Target = &browserTarget{Kind: "index", Label: "Log group", Option: "logGroup"}
		d.InitialOptions = map[string]any{"start": "now-1h", "limit": "200"}
		d.ResultSort = &browserResultSort{Key: "timestamp", Dir: "desc"} // default query sorts @timestamp desc
	case models.ConnectionTypeGCP:
		// One connection type, two providers: Cloud Logging is the log browser,
		// so BigQuery stays reachable from a profile rather than from here.
		d.Provider, d.Language, d.QueryLabel, d.ResultView = "gcpcloudlogging", "text", "Cloud Logging filter", "logs"
		d.DefaultQuery = `severity >= "WARNING"`
		d.InitialOptions = map[string]any{"start": "now-1h", "limit": "200"}
		d.ResultSort = &browserResultSort{Key: "timestamp", Dir: "desc"} // logadmin.NewestFirst
	case models.ConnectionTypeAzure:
		d.Provider, d.Language, d.QueryLabel, d.ResultView = "azureloganalytics", "text", "KQL", "logs"
		d.DefaultQuery = "AzureActivity | top 100 by TimeGenerated"
		d.Target = &browserTarget{Kind: "index", Label: "Workspace", Option: "workspaceID"}
		d.InitialOptions = map[string]any{"start": "now-1h", "limit": "200"}
		d.ResultSort = &browserResultSort{Key: "timestamp", Dir: "desc"} // default query tops by TimeGenerated
	case models.ConnectionTypeKubernetes:
		// No query language — a pod-log request is entirely structural, so the
		// browser drives it from options alone.
		d.Provider, d.ResultView = "k8s", "logs"
		d.AllowEmptyQuery = true
		d.Target = &browserTarget{
			Kind: "kubernetes-workload", Label: "Workload",
			Kinds: []string{"pod", "deployment", "statefulset", "daemonset"},
		}
		d.InitialOptions = map[string]any{"limit": "200"}
		// The kubelet API resumes from a timestamp and nothing else, so the walk
		// is ascending and a limit returns the oldest lines — see
		// assertAscendingLogOrder in query/providers/k8slogs.go.
		d.ResultSort = &browserResultSort{Key: "timestamp", Dir: "asc"}
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
