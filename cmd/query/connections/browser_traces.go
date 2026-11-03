package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/tracequery"
)

func (h *connectionBrowserHandler) serveTraces(w http.ResponseWriter, r *http.Request, conn *models.Connection, tail string) {
	if conn.Type != models.ConnectionTypeOpenSearch && conn.Type != models.ConnectionTypeOpenTelemetry {
		http.Error(w, "connection does not support OpenSearch traces", 400)
		return
	}
	if r.Header.Get("Accept") == "application/schema+json" {
		writeJSON(w, rpc.SchemaForStruct(tracequery.Request{}))
		return
	}
	request, err := browserTraceRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	request.Source = conn.ID.String()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	service := tracequery.Service{Read: func(ctx context.Context, request tracequery.Request, ids []string) ([]tracequery.Span, error) {
		queryContext := h.ctx.Wrap(ctx)
		next := queryContext.ConnectionResolver()
		queryContext = queryContext.WithConnectionResolver(func(ctx dbcontext.Context, ref string) (*models.Connection, error) {
			if ref == request.Source {
				return conn, nil
			}
			if next != nil {
				return next(ctx, ref)
			}
			return nil, nil
		})
		return tracequery.ReadOpenSearch(queryContext, request, conn, ids)
	}}
	var result any
	if tail == "/traces" {
		result, err = service.Search(ctx, request)
	} else {
		result, err = service.Trace(ctx, request, strings.TrimPrefix(tail, "/traces/"))
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, tracequery.ErrTraceNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, result)
}

func browserTraceRequest(r *http.Request) (tracequery.Request, error) {
	schema := rpc.SchemaForStruct(tracequery.Request{})
	body := map[string]any{}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return tracequery.Request{}, err
	}
	for key, items := range values {
		property, ok := schema.Properties[key]
		if !ok || len(items) != 1 {
			return tracequery.Request{}, fmt.Errorf("unknown or repeated trace parameter %q", key)
		}
		switch property.Type {
		case "integer":
			body[key], err = strconv.Atoi(items[0])
		case "object":
			var modes map[string]string
			err = json.Unmarshal([]byte(items[0]), &modes)
			body[key] = modes
		default:
			body[key] = items[0]
		}
		if err != nil {
			return tracequery.Request{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return tracequery.Request{}, err
	}
	var request tracequery.Request
	err = json.Unmarshal(data, &request)
	return request, err
}
