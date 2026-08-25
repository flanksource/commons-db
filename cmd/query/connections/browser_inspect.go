package connections

// Source inspection for the connection browser: the schema/catalog trees the
// query editor autocompletes from. Split out of browser.go.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
)

func (h *connectionBrowserHandler) serveInspection(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	refresh, err := inspectionRefresh(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := inspectionContext(r.Context(), conn.Type, 15*time.Second)
	defer cancel()
	inspection, err := h.inspectConnection(ctx, conn, r.URL.Query().Get("database"), r.URL.Query().Get("target"), r.URL.Query().Get("targetKind"), refresh)
	if err != nil {
		http.Error(w, sanitizeConnectionError(err, conn), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, inspection)
}

func inspectionRefresh(r *http.Request) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("refresh"))
	if raw == "" {
		return false, nil
	}
	refresh, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("inspection refresh must be true or false")
	}
	return refresh, nil
}

func inspectionContext(parent context.Context, connectionType string, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	if connectionType == models.ConnectionTypeClickHouse {
		return contextWithoutDeadline{Context: ctx}, cancel
	}
	return ctx, cancel
}

// WORKAROUND(clickhouse-readonly-deadline): Hide the deadline so clickhouse-go does not send max_execution_time to read-only users.
// Correct fix: clickhouse-go should expose a way to disable deadline-derived server settings while preserving client cancellation.
// Ref: https://github.com/ClickHouse/clickhouse-go/blob/v2.46.0/context.go#L222-L241
type contextWithoutDeadline struct {
	context.Context
}

func (contextWithoutDeadline) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (h *connectionBrowserHandler) inspectConnection(ctx context.Context, conn *models.Connection, database, targetName, targetKind string, refresh bool) (browserInspection, error) {
	switch conn.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse, models.ConnectionTypeSQLite:
		catalog, err := h.inspectSQL(ctx, conn, database, refresh)
		if err != nil {
			return browserInspection{}, err
		}
		return browserInspection{
			Kind: "sql", Dialect: sqlDialect(conn.Type), Database: catalog.Database, Databases: catalog.Databases,
			DefaultSchema: catalog.DefaultSchema, Schemas: catalog.Schemas, Nodes: catalogNodesForSQL(conn.Type, catalog),
			Truncated: catalog.Truncated, TruncateReason: catalog.TruncateReason, Cache: catalog.Cache,
		}, nil
	case models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch, models.ConnectionTypeOpenTelemetry:
		requestCtx := h.ctx.Wrap(ctx)
		searcher, err := h.inspectionOpenSearchSearcher(requestCtx, conn)
		if err != nil {
			return browserInspection{}, err
		}
		inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{
			CacheKey: searcher.InspectionKey(),
		})
		if err != nil {
			return browserInspection{}, err
		}
		targets, err := inspector.Targets(ctx, opensearchinspect.TargetRequest{Refresh: refresh})
		if err != nil {
			return browserInspection{}, err
		}
		inspection := browserInspection{Kind: "opensearch", Targets: targets.Targets, Nodes: catalogNodesForOpenSearch(targets.Targets), Truncated: targets.Truncated, TruncateReason: targets.TruncateReason, Cache: targets.Cache}
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
		fields, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{Target: *selected, Refresh: refresh})
		if err != nil {
			return browserInspection{}, err
		}
		inspection.Selected = &fields
		inspection.Cache = fields.Cache
		return inspection, nil
	default:
		return browserInspection{}, fmt.Errorf("connection type %q does not support inspection", conn.Type)
	}
}

func (h *connectionBrowserHandler) inspectionOpenSearchSearcher(ctx dbcontext.Context, conn *models.Connection) (*opensearch.Searcher, error) {
	if openSearchDirect(conn.Type) {
		return h.openSearchSearcher(ctx, conn)
	}
	outer, err := dbconnection.NewOpenTelemetry(conn)
	if err != nil {
		return nil, err
	}
	nested, err := outer.ResolveOpenSearch(ctx)
	if err != nil {
		return nil, err
	}
	return h.openSearchSearcher(ctx, nested)
}

func (h *connectionBrowserHandler) inspectSQL(ctx context.Context, conn *models.Connection, database string, refresh bool) (sqlinspect.Catalog, error) {
	key := fmt.Sprintf("%s:connection:%s:%d:%s:%s", h.ctx.ConnectionCacheScope(), conn.ID, conn.UpdatedAt.UnixNano(), conn.Type, database)
	result, err := h.sqlInspection.Get(ctx, inspection.GetOptions[sqlinspect.Catalog]{
		Key:     key,
		Refresh: refresh,
		Load: func(loadContext context.Context) (sqlinspect.Catalog, error) {
			client, err := h.sqlClient(loadContext, conn, database)
			if err != nil {
				return sqlinspect.Catalog{}, err
			}
			defer func() { _ = client.Close() }()
			return sqlinspect.Inspect(loadContext, client, conn.Type, sqlinspect.Limits{})
		},
	})
	if err != nil && !result.Cache.Cached {
		return sqlinspect.Catalog{}, err
	}
	result.Value.Cache = &result.Cache
	return result.Value, nil
}

func sqlCatalogWeight(catalog sqlinspect.Catalog) int {
	weight := len(catalog.Databases) + len(catalog.Schemas)
	for _, schema := range catalog.Schemas {
		weight += len(schema.Relations)
		for _, relation := range schema.Relations {
			weight += len(relation.Columns)
		}
	}
	return weight
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
