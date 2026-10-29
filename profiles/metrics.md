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

Focused local checks:

```sh
go test ./query/metrics
go test ./query/providers -run TestProviders -ginkgo.focus='structured metric profiles|CloudWatch batches|prometheus provider'
promtool test rules query/metrics/testdata/counter-resets.yaml
```

Run checks from the repository root. The promtool fixture demonstrates how summing counters before rate can hide resets. Provider integration tests use local HTTP fixtures and make no AWS calls.
