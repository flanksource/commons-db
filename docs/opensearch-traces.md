# OpenSearch traces

Query exposes a Traces tab on OpenSearch and OpenTelemetry connections. OpenTelemetry connections open the tab by default. Select the index pattern and flat or Jaeger format under Filters. The default is `otel-traces-*`, flat documents, the last hour, 100 traces and at least two spans.

Discovered index groups use wildcard targets such as `jaeger-span-*` in the connection browser and generated profiles. OpenSearch resolves matching indexes when a search starts, including rotations added after catalog discovery. Group counts describe discovered indexes; selecting an individual index queries only that index. Scroll paging keeps its snapshot and sends continuation and cleanup IDs in JSON request bodies so long IDs do not enlarge request URLs.

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

## Paging policy

OpenSearch and Elasticsearch connections accept `properties.paging_mode` (`scroll` or `pit`, default `scroll`) and `properties.paging_threshold` (a string integer from `0` through `10000`, default `"10000"`). Bounded reads at or below the requested-row threshold use normal `from`/`size` paging. Larger or unbounded complete reads use the configured backend cursor. The threshold measures requested rows, not the number of matching documents; export ceilings describe the whole read rather than its batch size. Zero always uses the configured backend cursor.

OpenSearch and OpenTelemetry profiles can independently override either setting under `provider.options`:

```yaml
provider:
  type: opensearch
  connection: connection://search
  options:
    index: jaeger-span-*
    pagingMode: pit
    pagingThreshold: 1000
query: '{"query":{"match_all":{}}}'
```

Omitted profile settings inherit from the connection; OpenTelemetry inherits from its nested OpenSearch connection. An offset continuation stays stateless, and a scroll/PIT continuation retains its context. Normal paging cannot pass the 10,000-row result window: start a complete read to use scroll/PIT at greater depths. Existing cursors must be restarted after upgrading to this cursor format.

## Profile render mode

Choose **Traces** in a profile's Render mode selector, or set `render: traces` in YAML:

```yaml
profile: Service traces
render: traces
provider:
  type: opentelemetry
  connection: connection://search
  options:
    index: otel-traces-*
```

The view groups the profile's loaded span rows by `trace_id`. Keep the provider's `trace_id`, `span_id`, `timestamp`, and `duration_ms` fields in the result. `parent_id`, `service`, `operation`, `status`, `kind`, `_resource`, `_span_attributes`, and `_attributes` supply tree structure and inspector details. Profile filters and paging remain available; traces can be incomplete because only loaded rows are grouped. Open a trace to see its waterfall, and select a span to inspect it. Trace and span selection are stored in the URL alongside the profile's filters.

`render: traces` is separate from `render: trace`, which identifies a live streaming session.
