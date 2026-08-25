package connections

// Query execution for the connection browser: decode the request, dispatch it
// to the right provider, and answer with rows or with an error that explains
// what ran. Split out of browser.go, which owns the routing and the types.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
)

func (h *connectionBrowserHandler) serveQuery(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	descriptor, ok := descriptorForConnection(conn.Type)
	if !ok || descriptor.Kind != "query" {
		http.Error(w, "connection does not support queries", http.StatusBadRequest)
		return
	}
	var request browserQueryRequest
	decoder := json.NewDecoder(r.Body)
	// Same strictness as the sibling /compile and /values endpoints: a field
	// this endpoint does not know is a caller's mistake, and silently dropping
	// it turns a typo into a query that quietly ignores what was asked for.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "decode browser query: "+err.Error(), http.StatusBadRequest)
		return
	}
	// A structured OpenSearch search stands in for the query text: the builder
	// sends the specification and lets the server compile it.
	structured := (descriptor.Provider == "opensearch" || descriptor.Provider == "opentelemetry") && request.Options["search"] != nil
	// k8s has no query language at all; jaeger's is optional.
	queryless := descriptor.Provider == "jaeger" || descriptor.Provider == "k8s"
	if strings.TrimSpace(request.Query) == "" && !queryless && !structured {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	for _, key := range []string{"url", "address", "type", "endpoint"} {
		delete(request.Options, key)
	}
	if descriptor.Provider == "http" {
		parsed, err := url.Parse(request.Query)
		if err != nil || parsed.IsAbs() || parsed.Host != "" {
			http.Error(w, "HTTP browser queries must be relative to the selected connection", http.StatusBadRequest)
			return
		}
	}
	// The console decides how much this query explains about itself, through the
	// level it armed the request at. A `debug` flag in the body used to say the
	// same thing, badly: it made every caller re-run the query to see what it
	// had already done.
	recorder := devtools.RecorderFromRequest(r)
	if recorder != nil {
		request.diagnostics = query.NewDiagnostics(query.DiagnosticOptions{
			Provider: descriptor.Provider, Query: request.Query, Options: request.Options,
			Detail: recorder.DiagnosticDetail(),
		})
	}

	started := time.Now()
	var result browserQueryResult
	var err error
	// The SQL and OpenSearch branches talk to their backends directly rather
	// than through a registered provider, so they never reach the seam that
	// files an operation — they are registered here instead. The default branch
	// does reach it, and registering it here too would report every one of its
	// queries twice.
	var operation *query.Operation
	switch descriptor.Provider {
	case "postgres", "mysql", "sqlserver", "clickhouse", "sqlite":
		operation = recorder.Operation(descriptor.Provider)
		database, _ := request.Options["database"].(string)
		result, err = h.executeSQL(r, conn, descriptor, request, database)
	case "opensearch":
		operation = recorder.Operation(descriptor.Provider)
		result, err = h.executeOpenSearch(r, conn, descriptor, request)
	default:
		var provider query.Provider
		provider, err = query.GetProvider(descriptor.Provider)
		if err == nil {
			providerStarted := time.Now()
			result.Rows, err = provider.Execute(query.WithRecorder(h.ctx, recorder), query.ProviderRequest{
				Provider: descriptor.Provider, Connection: conn.ID.String(), Query: request.Query,
				Options: request.Options, Diagnostics: request.diagnostics,
				Inspection: query.InspectionOptions{Refresh: request.RefreshInspection},
			})
			if len(result.Rows) > 0 {
				request.diagnostics.RecordPreview("application/json", query.MarshalDiagnosticPreview(result.Rows))
			}
			request.diagnostics.RecordResponse(providerStarted, len(result.Rows), nil)
		}
	}
	elapsed := time.Since(started)
	result.DurationMS = float64(elapsed.Microseconds()) / 1000
	operation.Complete(query.OperationResult{
		Diagnostics: request.diagnostics, Duration: elapsed, Rows: len(result.Rows), Err: err,
	})
	if err != nil {
		writeBrowserQueryError(w, err, request.diagnostics, http.StatusUnprocessableEntity)
		return
	}
	result.Diagnostics = request.diagnostics.Snapshot()
	writeJSON(w, result)
}

func writeBrowserQueryError(w http.ResponseWriter, err error, diagnostics *query.ProviderDiagnostics, status int) {
	if fromError := query.DiagnosticsFromError(err); fromError != nil {
		diagnostics = fromError
	} else if diagnostics != nil {
		diagnostics.RecordError(err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error       string                     `json:"error"`
		Diagnostics *query.ProviderDiagnostics `json:"diagnostics,omitempty"`
	}{Error: err.Error(), Diagnostics: diagnostics.Snapshot()})
}

func (h *connectionBrowserHandler) executeSQL(
	r *http.Request,
	conn *models.Connection,
	descriptor browserDescriptor,
	request browserQueryRequest,
	database string,
) (browserQueryResult, error) {
	statement := request.Query
	// Whether the connection may be written to is settled before anything is
	// dispatched, because it is a property of the connection and the statement
	// alone. Asking it per branch made it a property of which branch the
	// statement happened to take, and the branch is chosen by a keyword.
	if err := readOnlyStatementError(conn, statement); err != nil {
		return browserQueryResult{}, err
	}
	pageRequest, err := request.Pagination.PageRequest()
	if err != nil {
		return browserQueryResult{}, err
	}
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
	if !sqlReturnsRows(statement) {
		if len(filters) > 0 {
			return browserQueryResult{}, fmt.Errorf("column filters can only be applied to a row-producing SQL query")
		}
		started := time.Now()
		request.diagnostics.RecordRequest(statement, nil, map[string]any{"dialect": conn.Type, "operation": "exec"})
		// The statement is the operator's own, typed into a SQL console that
		// exists to run it, and it reaches the driver whole — no request value is
		// interpolated into it, so there is no second command to inject.
		//
		// CodeQL reports this as go/sql-injection (alert 70) and is right about
		// the dataflow and wrong about the boundary: an HTTP body does reach a
		// SQL sink, because that is the product. It has to be dismissed in the
		// Security tab. A `// codeql[go/sql-injection]` comment used to sit on
		// this line and was doing nothing — the alert stayed open on the line
		// directly beneath it — so it is gone rather than left here reading like
		// a suppression that holds. What the console does enforce is the
		// connection's own terms; see readOnlyStatementError below.
		res, err := client.ExecContext(r.Context(), statement)
		if err != nil {
			request.diagnostics.RecordError(err)
			request.diagnostics.RecordResponse(started, 0, map[string]any{"dialect": conn.Type, "operation": "exec"})
			return browserQueryResult{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			request.diagnostics.RecordResponse(started, 0, map[string]any{"dialect": conn.Type, "operation": "exec"})
			return browserQueryResult{Message: "Statement executed successfully"}, nil
		}
		request.diagnostics.RecordResponse(started, 0, map[string]any{
			"dialect": conn.Type, "operation": "exec", "affectedRows": affected,
		})
		return browserQueryResult{AffectedRows: &affected, Message: "Statement executed successfully"}, nil
	}

	page, err := providers.ReadSQLPage(r.Context(), client, conn.Type, providers.SQLPageRequest{
		Query: statement, Filters: filters, Page: pageRequest, Diagnostics: request.diagnostics,
	})
	if err != nil {
		return browserQueryResult{}, err
	}
	databaseTypes := make(map[string]string, len(page.ColumnTypes))
	for _, column := range page.ColumnTypes {
		if name := sqlColumnTypeName(column.DatabaseTypeName()); name != "" {
			databaseTypes[column.Name()] = name
		}
	}
	// The result's own columns describe it, so a filtered run keeps offering the
	// filters that produced it.
	described := browserProfile(descriptor, conn, request.Query, request.Options, sqlBrowserColumns(page.ColumnTypes))
	columns, err := describeBrowserColumns(described, databaseTypes)
	if err != nil {
		return browserQueryResult{}, err
	}
	pageInfo := query.NewPageInfo(pageRequest, query.Page{HasMore: page.HasMore, Total: page.Total})
	return browserQueryResult{
		Rows: page.Rows, Columns: columns,
		Pagination: &pageInfo,
	}, nil
}

// readOnlyStatementError reports why statement may not run on conn, or nil when
// it may.
//
// A read-only connection is one the console must not write through, which is a
// stronger claim than "the driver was opened read-only": the snapshot DSN that
// backs today's read-only connections would refuse the write anyway, but the
// refusal an operator should read is this one, and it must hold for whichever
// connection carries the flag next.
func readOnlyStatementError(conn *models.Connection, statement string) error {
	if !conn.ReadOnly {
		return nil
	}
	if err := query.ValidateReadOnlySQL(statement); err != nil {
		return fmt.Errorf("connection %q is read-only: %w", conn.Name, err)
	}
	return nil
}

// sqlReturnsRows reports where a statement is dispatched — QueryContext for one
// that produces a result set, ExecContext for one that reports a row count.
//
// It answers that and only that. It is a keyword match on the opening word, so
// it cannot see whether a statement writes: `WITH gone AS (DELETE ...) SELECT`
// opens with a row-producing keyword and deletes. Whether a write is permitted
// is readOnlyStatementError's question, asked before any dispatch.
func sqlReturnsRows(statement string) bool {
	statement = strings.ToLower(strings.TrimSpace(statement))
	for _, prefix := range []string{"select", "with", "show", "describe", "desc", "explain", "pragma", "values"} {
		if strings.HasPrefix(statement, prefix) {
			return true
		}
	}
	return false
}
