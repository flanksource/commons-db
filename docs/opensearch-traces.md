# OpenSearch traces

Query exposes a Traces tab on OpenSearch and OpenTelemetry connections. OpenTelemetry connections open the tab by default. Select the index pattern and flat or Jaeger format under Filters. The default is `otel-traces-*`, flat documents, the last hour, 100 traces and at least two spans.

Search filters apply to complete traces. Includes are any-of within a service, operation or status filter; exclusions reject matching traces. Attribute includes are any-of per key and all-of across keys. Enter `db.system`, `db.system=postgresql` or `http.route=/orders/*` to match presence, equality or a wildcard. Root-operation counts omit the operation and span-count filters so another operation remains discoverable.

Open a result or scatter point to inspect its waterfall. Selecting a span updates the URL. Consecutive database calls group by default; expand the group or use the arrow keys to inspect individual calls. Missing parents remain visible as a partial trace.

## HTTP API

Both endpoints are GET requests, scoped to the connection named in the path:

```sh
curl 'http://localhost:8080/api/v1/connection/search/browser/traces?index=otel-traces-*&lookback=1h&minSpans=2'
curl 'http://localhost:8080/api/v1/connection/search/browser/traces/0123456789abcdef0123456789abcdef?index=otel-traces-*'
```

For Jaeger indexes, add `format=jaeger&index=jaeger-span-*`. Request `Accept: application/schema+json` on the search endpoint for the query schema. The server's OpenAPI document includes both result schemas. Limits are applied after grouping; exceeding the bounded span read is an error, never a sampled count.

Telltale uses the same views and query implementation at `/otel` and `/otel/:traceId`, retaining its Received and Recorded sources alongside configured OpenSearch connections.
