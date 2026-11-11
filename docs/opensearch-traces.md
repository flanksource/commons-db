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

The server normalizes source spans before aliases and column mapping. It recognizes canonical fields, Jaeger IDs, references and microsecond durations, and OTLP timestamps and explicit duration units. Canonical mappings live under `spans`; superseded provider field mappings are removed. Invalid or ambiguous spans return an error. Aliases and CEL columns can read canonical fields such as `span_id`, `timestamp`, and `duration_ms`.

The Spans editor maps source values into the canonical model used by the waterfall and inspector. Omit a target for automatic detection, or configure `source`, `cel`, or `jsonpath` (with an optional source root). Numeric timing sources accept `unit: s`, `ms`, `us`, or `ns`:

```yaml
spans:
  service:
    source: process.serviceName
  durationMs:
    source: duration
    unit: us
```

Listing columns are selected from canonical fields and normalized span/resource attributes. **Reset to default** restores timestamp, service, operation, duration and status, with hidden trace/span/parent IDs, without requiring a sample. Sampling does not append provider fields. Attribute columns use literal-key JSONPaths into `_span_attributes` or `_resource`, preserving names such as `http.method`. Existing raw column sources require correction or reset; there is no compatibility translation. Changes remain drafts until Save profile.

The listing uses the profile's configured columns, labels and formats. Canonical spans travel separately in hidden `_trace` metadata, so hiding or renaming a display column does not lose waterfall or inspector details. Profile filters and paging remain available; traces can be incomplete because only loaded rows are grouped. Open a trace to see its waterfall, and select a span to inspect it. Trace and span selection are stored in the URL alongside the profile's filters.

The editor previews the same listing and details from retained sample input. Mapping edits call `POST /api/v1/profile/sample/mapping` with `{ "profile": <draft>, "rows": <mappingInputRows> }`; this applies normalization, aliases and columns without querying the provider. The response includes pre-alias `normalizedRows`, the normalized `availableColumns` catalogue, mapped `rows`, `resultColumns`, and the formatted Clicky `document`. Run a new sample after changing the source query or processors. Direct canonical source mappings also resolve backend filter fields; computed mappings require an explicit backend filter field.

For local sibling-source verification, run `make -C cmd/query build UI_BUILD_MODE=development PNPM='corepack pnpm@11.22.0'`, then `.bin/query serve -p 8086 --dev`. Production builds use the published clicky-ui package.

`render: traces` is separate from `render: trace`, which identifies a live streaming session.

## Portable span conditions

In **Source & Query**, choose **Use canonical conditions** to import the current native query. The editor keeps logical conditions in the profile's top-level `search`, and previews the OpenSearch query resolved for the selected connection. Conversion only changes the draft. Changing a connection re-inspects its index mappings and preserves the logical conditions and parameter references.

```yaml
profile: Slow checkout spans
render: traces
provider:
  type: opensearch
  connection: connection://traces
search:
  size: 100
  query:
    op: bool
    conditions:
      - op: term
        field: service
        value: checkout
      - op: range
        field: duration_ms
        gte: 250
```

Set `properties.index` on each connection (for example `jaeger-span-*` or `otel-traces-*`) to reuse this profile without a connection-specific index in the profile. `provider.options.index` is an explicit profile override. OpenTelemetry connections can inherit the index and layout from their nested OpenSearch connection.

Canonical query fields are `trace_id`, `span_id`, `parent_id`, `timestamp` (RFC3339), `duration_ms` (milliseconds), `service`, `operation`, `status`, and `kind`. Attributes use `attributes.http.method` and resources use `resource.service.name`, with the suffix treated as a literal attribute key. Mapped Jaeger, canonical and flat OTLP fields resolve automatically, including keyword multifields, known timestamp/duration units and status encodings. Queries, sorts, listing filters and returned spans use the same resolved layout. Normalization runs before aliases and column mapping.

Jaeger indices that keep attributes in the nested `tags` and `process.tags` key/value lists (no tags-as-fields) resolve `attributes.<key>` and `resource.<key>` through nested queries: term, terms, match, prefix, wildcard and exists filters, listing include/exclude, column sorts and facet lookups all pin the entry by its key and read its `value`. Facet counts are spans, not tag entries. A scalar mapping such as Jaeger's `tag.db@operation` wins over the nested list when both exist. Ranges on nested attributes return an error, because the values are indexed as keyword strings and would compare lexically. For typed numeric ranges, enable Jaeger `--es.tags-as-fields.all` (or an allowlist) or flatten the tags with an ingest pipeline; both apply only to newly written indices.

For custom source names or units, set `properties.span_layout` on the connection to a JSON string such as:

```json
{"fields":{"trace_id":{"source":"ids.trace"},"span_id":{"source":"ids.span"},"timestamp":{"source":"clock","unit":"ns"},"duration_ms":{"source":"elapsed","unit":"us"},"service":{"source":"workload"}}}
```

Each entry's `source` identifies the document path. Optional `query` selects an exact-match keyword multifield; `unit` specifies numeric time units (`s`, `ms`, `us`, `ns`); `values` maps canonical enum values to native values. Live mappings validate these paths. Keep connection-specific layouts on connections so the same logical profile can move between them. Computed CEL/JSONPath span mappings and aggregate attribute/resource mappings cannot be inverted into source conditions and return an error for portable searches; configure their individual fields on the connection instead.

Other key/value attribute lists can be declared in the same JSON with `nested` rules, for example `{"nested":[{"prefix":"attributes.","path":"labels","key":"labels.name","value":"labels.value"}]}`. The prefix must be `attributes.` or `resource.`, and the key and value must be keyword leaves of the nested `path`.

`POST /api/v1/profile/sample/query` accepts `{ "profile": <draft>, "direction": "source" | "canonical", "query": <optional raw DSL>, "params": <parameter values> }`. It inspects metadata without searching documents and returns the canonical structured `search`, canonical/native DSL and resolved layout. The endpoint imports supported boolean, term, range, existence and text conditions. Missing or ambiguous fields, nested queries, embedded query-string field references, unsupported source projections/aggregations, and non-invertible source-specific parameter values produce explicit errors. Top-level `search` is mutually exclusive with raw `query` and native `provider.options.search`.
