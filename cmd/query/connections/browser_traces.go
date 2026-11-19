package connections

import (
	"context"
	"net/http"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/tracequery"
)

func (h *connectionBrowserHandler) serveTraces(w http.ResponseWriter, r *http.Request, conn *models.Connection, tail string) {
	if conn.Type != models.ConnectionTypeOpenSearch && conn.Type != models.ConnectionTypeOpenTelemetry {
		http.Error(w, "connection does not support OpenSearch traces", 400)
		return
	}
	service := tracequery.Service{Resolve: func(ctx context.Context, _ string) (dbcontext.Context, *models.Connection, error) {
		return withBrowserConnection(h.ctx.Wrap(ctx), conn), conn, nil
	}}
	traces := r.Clone(r.Context())
	traces.URL.Path, traces.URL.RawPath = strings.TrimPrefix(tail, "/traces"), ""
	tracequery.Handler(service, conn.ID.String()).ServeHTTP(w, traces)
}
