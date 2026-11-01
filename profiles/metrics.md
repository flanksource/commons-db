# Metric summary profiles

`container-metrics`, `pod-metrics`, and `rds-sqlserver-metrics` return one row per resource and metric for the selected range. Statistics are numeric columns: `min`, `avg`, `max`, `p50`, `p90`, `p95`, `p99`, and `samples`. The `unit` column applies to every statistic in its row. `samples` counts evaluation points, not requests, IO operations, or original scrapes.

Configure saved connections named `prometheus` and `aws`, then run the profiles from the query CLI. The YAML examples live in this directory. For a standalone Prometheus server, clear `provider.connection` and set `provider.options.url` to its address. For AWS's default credential chain, clear `provider.connection` and supply the region using `provider.options.region` or `AWS_REGION`.

```sh
go run . --db='' --profiles-dir ../../profiles profiles run container-metrics --all
go run . --db='' --profiles-dir ../../profiles profiles run pod-metrics --all
go run . --db='' --profiles-dir ../../profiles profiles run rds-sqlserver-metrics --all
```

Run these commands from `cmd/query`. AWS metric reads incur the normal AWS charges. The profiles keep native PromQL available through `language: promql`; SQL is selected explicitly with `language: sql`. The provider remains `prometheus`, and `FROM` identifies the metric name.

## Labels and columns

Profile-level `labels` declares SQL column names mapped to upstream labels or dimensions:

```yaml
labels:
  cluster: cluster
  namespace: namespace
  pod: pod
  container: container
```

CloudWatch uses the same declaration, such as `db_instance: DBInstanceIdentifier`. These are input columns, distinct from the profile's ordered output `columns`. Missing labels are NULL, present empty labels remain empty, and full upstream labels still identify each original series. A projection never deduplicates HA replicas or repeated scrape jobs. Select one scrape job or configure backend deduplication, and retain cluster identity when combining resources.

## Rates and statistics

SQL `rate(value, '5m')` and `increase(value, '5m')` execute in Prometheus with native counter reset and extrapolation behavior. A table alias uses one transformation/window; use separate aliases for different source transformations. Raw values and transformed values cannot be mixed on the same alias.

For pod totals, aggregate container rates at each timestamp before calculating statistics over time. Summing rates over successive timestamps is not an event count. Summing container p95 values is not the pod p95. The pod profile groups simultaneous observations first; the container profile retains each container.

`percentile(value, 95)` and `percentile_cont(value, 0.95)` use continuous interpolation over all non-NULL rows in a group. `median(value)` is p50. They are exact for the returned evaluation samples, use memory proportional to the population, and depend on the configured evaluation step. Query range, counter window, evaluation step, and optional SQL bucket width are separate choices. Resolution is never silently coarsened.

Request latency percentiles require the observation distribution. Use native PromQL for classic histograms:

```promql
histogram_quantile(0.95,
  sum by (service, le) (rate(http_request_duration_seconds_bucket[5m])))
```

For native histograms:

```promql
histogram_quantile(0.95,
  sum by (service) (rate(http_request_duration_seconds[5m])))
```

Raw native histogram rows are rejected. Summary quantile series cannot be combined into a fleet observation percentile. Mean request latency uses total duration divided by total count, not the average of per-instance means.

## CloudWatch retrieval

`cloudwatchmetrics` supports `language: metrics`, `language: sql`, and `language: metrics-insights`. Metric batches contain up to 500 query structures and follow continuation tokens. `concurrency` defaults to four and accepts 1–32. Request cancellation and SDK retries apply to every batch. Terminal partial responses, missing query completion, repeated continuation tokens, and backend messages fail the query.

SQL sources use `namespace`, `statistic` (default `Average`), and `range: {start, end, step}`. `step` becomes the CloudWatch period and must satisfy retention constraints. `dimensions` narrows discovery; SQL predicates still determine the returned rows. ListMetrics discovery excludes inactive metrics and can lag newly created metrics, so query metadata includes a notice.

An optional `unit` selects the unit published to CloudWatch; it is a filter and performs no conversion. Explicit metric identities can override both statistic and unit.

For historical completeness, specify complete metric identities in `options.metrics`; SQL then uses those identities instead of discovery:

```yaml
metrics:
  - name: ReadLatency
    namespace: AWS/RDS
    dimensions: {DBInstanceIdentifier: acme-sqlserver}
    statistic: Average
```

In native batch mode, list multiple statistics such as `Sum` and `SampleCount` explicitly when calculating weighted averages. Metrics Insights uses native AWS query syntax, supports at most two weeks of history and periods of at least 60 seconds, and refuses a result at the series ceiling because completeness cannot be established.

RDS CPU values are percentages; FreeableMemory is available memory converted to MiB; ReadLatency and WriteLatency are converted from seconds to milliseconds; IOPS already measures operations per second. Latency summary rows explicitly identify period averages: their p95 is not the p95 of individual IO operations. `rds-sqlserver-failed-jobs` imports the RDS profile with `Sum` statistics and summarizes failed jobs per period. Gaps remain missing rather than becoming zero failures.

## Buckets, gaps, and paging

`time_bucket('5m', time)` creates fixed UTC buckets. `first(value, time)` and `last(value, time)` order by timestamps and reject ambiguous tied values. Standard SQLite windows are available. `time_series(start, end, step)` generates a half-open timestamp grid for explicit gap filling:

```sql
SELECT grid.time, source.value
FROM time_series('2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z', '1m') AS grid
LEFT JOIN my_metric AS source ON source.time = grid.time
ORDER BY grid.time
```

Use `coalesce` only when zero is meaningful for the metric. Apply filling after the statistics whose input population must preserve gaps.

Source samples stream into a private SQLite workspace. Complete summary results are stored in SQLite for cursor paging, with a five-minute idle expiry. Resume using the returned cursor to avoid repeating upstream reads; a fresh offset request starts a fresh execution. Cursors are scoped to caller, connection, SQL, parameters, labels, filters, and order. They expire when the process restarts. Query metadata travels in CLI results and the API's `X-Metric-Metadata` header.

## Predicate pushdown

`WHERE` conjuncts on a label column of a `FROM <metric>` table narrow what the source fetches. The `prometheus` provider adds them to the metric selector:

| SQL | Prometheus matcher |
| --- | --- |
| `pod = 'api'` | `pod="api"` |
| `pod != 'api'` | `pod!="api"` |
| `pod IN ('api', 'web.1')` | `pod=~"^(api\|web\.1)$"` |
| `pod NOT IN ('api')` | `pod!~"^(api)$"` |
| `pod LIKE 'api-%'` | `pod=~"(?i)^api-.*$"` |
| `pod NOT LIKE 'api-%'` | `pod!~"(?i)^api-.*$"` |

Values are regex-escaped; `%` becomes `.*` and `_` becomes `.`. LIKE folds case because SQLite's LIKE does. Bound parameters (`$(.params.pod)`) are pushed the same way when they are strings. Only `AND`-joined conjuncts are pushed, so `OR` and other expressions stay in SQLite. SQLite still evaluates the whole `WHERE` clause after loading, so pushdown never changes the result. It only reduces what is fetched. `cloudwatchmetrics` pushes `=` into dimension filters and leaves the other operators to SQLite.

Filter value lookups list a label column's values through `/api/v1/label/<label>/values`. The column is mapped through `labels`, and the lookup is scoped with `match[]` to the metrics the statement reads.

## PromQL as a table: `prometheus(...)`

When a single PromQL expression is easier than a metric-table query, call it as a table function:

```sql
SELECT time, json_extract(labels, '$.pod') AS pod, value
FROM prometheus('sum by (pod) (rate(container_cpu_usage_seconds_total[5m]))')
```

The columns are `time`, `value`, `series_id`, `metric_name` (from `__name__`) and `labels`, a JSON object of the remaining labels. `prometheus(query, start, end, step)` overrides the profile's `range`. Pass `start` and `end` as RFC3339 timestamps and `step` as a duration. The query can be a bound parameter. A range wider than 9999 steps is fetched in chunks. Each evaluation is reported in the query metadata. An identical call inside one statement is fetched once, so the function can sit on the inner side of a join.

`prometheus` and `time_series` are reserved names, not metric names. Only the `prometheus` provider can evaluate PromQL, so `prometheus(...)` under `cloudwatchmetrics` fails with an error that says so. Never splice a param into the PromQL text. Match it against `labels` in `WHERE` instead, as `pod-cpu-by-pod.yaml` does.

## Profiles over profiles: `profiles.<name>`

Metric SQL can read another profile's result as a table:

```sql
SELECT pod, max(value) AS peak
FROM profiles."pod-cpu-by-pod"
GROUP BY pod
```

The referenced profile runs through the normal engine. It receives each of the parent's resolved params that it declares by name, and its own defaults fill in the rest. Its rows load into a temporary table. The table's columns are the profile's declared `columns`, or else the keys its rows carry. Quote a profile name that contains `-` or `.`.

A reference is resolved by the profile service that runs the parent, on every read it serves: the execute route, `run`, listing, inspect, replay, reconcile, filter lookups, the sample and sample filter-value routes, series and scheduled queries. That service's read hook prepares it under the caller's context. A profile can therefore only read data its caller could read directly. A reference back to a profile that is already running fails with the cycle's path, such as `profile reference cycle: a -> b -> a`. A database-qualified name in any other schema is still rejected.

## Series: graph-ready output

`series:` projects a profile's rows onto plottable series. Each label set becomes one line, and its points are sorted by time:

```yaml
series:
  time: time        # default: time, else timestamp (the column PromQL rows carry)
  value: value      # default: value
  labels: [pod]     # default: every column except time and value
  metric: metric    # optional column holding a metric name; always part of a series' identity
  unit: millicores  # clicky-ui unit key
  label: "{{.pod}}" # Go template over the series labels
```

A series id is `<source>#<k=v,...>` over its sorted labels. When the result has a single series with no labels, the id is the source itself. A value that is not numeric, a NULL value or a time that does not parse fails the projection. It is never dropped silently. A profile with `series:` renders as `timeseries` (`x-clicky-render: timeseries`) unless `render:` says otherwise.

Metrics panels read a series profile as `profile:<name>?k=v&...`. The profile service's `SeriesSource(SeriesSourceOptions{Scope, Timeout})` serves that id prefix to clicky's metrics stream. For each tick it replaces the profile's `range` with the panel window, aligned down to the step, so timestamps stay stable between ticks. A profile without a `range` is refused. It then runs the read hook under the caller's context and executes the profile. Identical requests in a tick share one execution when their `Scope` is equal too. `Scope` names whatever a read takes from its caller's context: the environment a connection resolves in, the tenant, and the principal when the read hook authorizes per caller. A shared execution keeps running when one caller leaves, so each caller waits under its own context, and `Timeout` bounds the execution. The connection browser's Prometheus console returns the same projection in `series`, and it takes `labels` in the request body for `language: sql`.

`pod-cpu-by-pod.yaml` is a runnable example:

```sh
go run . --db='' --profiles-dir ../../profiles profiles run pod-cpu-by-pod --param namespace=kube-system --all
```

Focused local checks:

```sh
go test ./query/metrics
go test ./query/providers -run TestProviders -ginkgo.focus='structured metric profiles|CloudWatch batches|prometheus'
go test ./query -run TestQuery -ginkgo.focus=ProjectSeries
promtool test rules query/metrics/testdata/counter-resets.yaml
```

Run checks from the repository root. The promtool fixture demonstrates how summing counters before rate can hide resets. Provider integration tests use local HTTP fixtures and make no AWS calls.
