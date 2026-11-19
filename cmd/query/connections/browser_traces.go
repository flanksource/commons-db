package connections

import (
	"context"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/tracequery"
)

func (h *connectionBrowserHandler) serveTraces(w http.ResponseWriter, r *http.Request, conn *models.Connection, tail string) {
	if conn.Type != models.ConnectionTypeOpenSearch && conn.Type != models.ConnectionTypeOpenTelemetry {
		http.Error(w, "connection does not support OpenSearch traces", 400)
		return
	}
	service := tracequery.Service{Read: func(ctx context.Context, request tracequery.Request, ids []string) ([]tracequery.Span, error) {
		queryContext := withBrowserConnection(h.ctx.Wrap(ctx), conn)
		return tracequery.ReadOpenSearch(queryContext, request, conn, ids)
	}}
	traces := r.Clone(r.Context())
	traces.URL.Path, traces.URL.RawPath = strings.TrimPrefix(tail, "/traces"), ""
	tracequery.Handler(service, conn.ID.String()).ServeHTTP(w, traces)
}
