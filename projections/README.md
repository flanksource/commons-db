# Projections

Project JSON from local commands or HTTP APIs into existing YAML files, using CEL for matching and mapping and JSONPath for field selection. Comments, flow collections and indentation are preserved. The engine is copied from Faro; Faro keeps its native Mission Control sources and authentication.

## CLI workflow

```bash
query projections verify projections.yaml
query projections run projections.yaml --context staging --json --no-progress
query projections apply projections.yaml --context staging --dry-run
query projections apply projections.yaml --context staging
query projections syntax --help --no-color
```

`verify` checks manifests, CEL, JSONPaths, existing targets and optional schemas offline. It never runs commands, resolves credentials or makes HTTP requests. `run` executes sources and returns `projection`, `context`, `items`, `filtered` and `warnings`. `apply --dry-run` executes sources and evaluates mappings without writing targets. `apply` writes successful projections sequentially and continues after individual failures; any failure makes the command exit nonzero. Successful earlier writes remain on disk.

All three commands accept positional files/directories, repeatable `--file/-f`, and `--name` to select an exact `metadata.name`. Directories are searched recursively for `.yaml` and `.yml` in lexical order, skipping foreign documents. Explicit files must contain Projections. Use `---` between documents. Names must be unique across all loaded files. Paths are relative to each manifest. `--context` supplies `context.name`; it does not load a query profile or choose a Faro context. `context.observed_at` and `context.date` are generated per run. Output flags include `--json`, `--yaml`, `--csv`, `--markdown`, `--no-progress` and `--no-color`; `NO_COLOR` also disables color.

## Exec: once per projection

Save the following as `projections.yaml` beside a `target.yaml` containing `entries: []`:

```yaml
apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: inventory
spec:
  source:
    query:
      exec:
        mode: once
        script: printf '%s' '{"items":[{"id":"api","name":"API service"}]}'
        select: $.items
        timeout: 1m
    where: source.id != ""
  target:
    path: target.yaml
    select: $.entries[*]
  match:
    - source.id == target.id
  set:
    $.id:
      value: source.id
    $.name:
      value: source.name
    $.observed:
      value: context.date
```

`script` is required. It runs locally with the shared shell runner, in the manifest's directory; a shebang selects an interpreter. `mode` defaults to `once`, and `timeout` defaults to one minute per invocation, including credential setup. No commands are retried. Standard output must contain exactly one JSON object or array of objects, after optional `select`. Scalars, null, mixed arrays, malformed JSON, timeouts and unsuccessful exit codes fail the projection before mappings are applied. Empty arrays are valid.

`select` is an optional JSONPath into command output. It must match exactly one value, which can itself be an array; use `$.items`, not `$.items[*]`. Diagnostic output belongs on stderr.

## Exec: once per target

Use this target as `target.yaml`:

```yaml
entries:
  - {id: api, active: true}
  - {id: worker, active: true}
  - {id: archived, active: false}
```

```yaml
apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: target-facts
spec:
  source:
    query:
      exec:
        mode: perTarget
        script: printf '{"state":"ready","id":"%s"}' {{shellQuote .target.id}}
        timeout: 30s
  target:
    path: target.yaml
    select: $.entries[*]
    filter: target.active
    create: false
  match:
    - source.id == target.id
  set:
    $.state:
      value: source.body.state
```

`perTarget` requires a target. Eligible entries run sequentially in document order; `target.filter` is evaluated before fan-out. Each output may be any JSON value and becomes `source.body`; target scalar fields are copied onto the source row for matching. A failure aborts that projection before any mappings are written. Scripts are rendered before execution, using Go templates with `missingkey=error`. `.context` is available in both modes; `.target` is available only in `perTarget`. Use `{{shellQuote .target.id}}` for shell arguments. The helper quotes strings for POSIX shells and PowerShell, rejecting unsupported interpreters. JSON serialization still needs a JSON-aware tool when arbitrary values are emitted.

## Local connections and environment

Named connections are read only from `$XDG_CONFIG_HOME/flanksource/connections/`, or `~/.config/flanksource/connections/` when XDG is unset. The existing connection store reads typed YAML/JSON Connection resources and handles bare names, `connection://name`, `connection://namespace/name` and IDs. Missing or ambiguous names fail locally. No database or remote connection lookup occurs.

For example, save this resource in that directory as `inventory.yaml`:

```yaml
apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata:
  name: inventory
  namespace: examples
spec:
  aws:
    region: eu-west-1
    accessKey:
      value: $(INVENTORY_ACCESS_KEY)
    secretKey:
      value: $(INVENTORY_SECRET_KEY)
```

A source-only projection using that connection:

```yaml
apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: aws-inventory
spec:
  source:
    query:
      exec:
        script: aws ec2 describe-instances --output json
        connections:
          aws:
            connection: connection://examples/inventory
        env:
          - name: AWS_PAGER
            value: ""
        passthroughEnv: [XDG_CONFIG_HOME]
        select: $.Reservations
        timeout: 1m
```

`connections` uses shared typed `aws`, `kubernetes`, `gcp`, `azure` and `opensearch` settings. Inline settings and native EnvVar references are supported. `fromConfigItem` is unsupported. AWS provides a temporary credentials file and region; Kubernetes provides `KUBECONFIG`; GCP provides `GOOGLE_APPLICATION_CREDENTIALS` and an isolated `CLOUDSDK_CONFIG`; Azure logs in using an isolated `AZURE_CONFIG_DIR`. OpenSearch provides `OPENSEARCH_URLS` (a JSON array), `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD`, `OPENSEARCH_INDEX` and `OPENSEARCH_INSECURE_SKIP_VERIFY`.

`env` accepts named shared EnvVar values or references. `passthroughEnv` names additional host variables to pass; the runner already passes basic variables such as HOME and PATH. Credentials and auth caches are created under the manifest directory's `.tmp` and removed after execution, including failed and cancelled invocations. Captured output is redacted by the shared shell runner.

## HTTP: once per target

Use the same target entries above. Each request URL is a Go template over the target's fields directly:

```yaml
apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: api-facts
spec:
  source:
    query:
      http:
        url: https://api.example.com/inventory/{{.id}}
        method: GET
        bearer:
          value: $(INVENTORY_TOKEN)
        select: $.data
        accepted:
          attempts: 5
          wait: 2s
  target:
    path: target.yaml
    select: $.entries[*]
    filter: target.active
    create: false
  match:
    - source.id == target.id
  set:
    $.state:
      value: source.body.state
```

`http` requires `target` and `url`. It accepts shared HTTPConnection authentication, including a named `connection`, basic auth, bearer, OAuth and TLS settings. Requests have a 30-second timeout and default to GET. Each response becomes `source.body`, with `source.url` and the target's scalar fields. `select` must match exactly one value. HTTP 202 is retried up to `attempts` (default 5), waiting `wait` (default 2s). Other failed requests, malformed responses and exhausted 202 waits omit the row and emit a warning. Cancellation stops the source. Verify validates URL templates against existing entries without requests.

## Matching, mapping and validation

`source.query` contains exactly one of `exec` or `http`. `source.where` is an optional CEL predicate over each row and `context`; rejected rows count as `filtered`. Source-only documents omit `target`, `match` and `set`.

`target.path` names an existing YAML file. `target.select` is a JSONPath ending in `[*]` selecting mapping entries. `target.filter` limits eligible targets. `target.schema` optionally names a local JSON Schema; the complete target is checked before and after mapping. Verify checks the existing document, not future source data.

`match` contains CEL predicates over `source`, `target` and `context`; any true expression matches. A source matching multiple targets fails. Unmatched sources create entries by default; `target.create: false` reports them as missing. Filtered eligible targets without source matches are reported as stale and retained.

Each `set` key is a JSONPath relative to the selected target, such as `$.name`. `value` is a CEL expression; `when` optionally guards evaluation. Use `value: '"managed"'` for a CEL string literal and `when: '"email" in source'` for optional fields. Mapping guards see the original target for that source row.

`strategy` defaults to `replace`. `mergeUnique` appends new list values not already present. `replaceMatching` removes existing list items satisfying its `match`, then inserts incoming values; `item` denotes each existing list element, for example `item.context == context.name`.

`target.aggregate: true` permits multiple sources to share a target. Use list strategies or guarded scalar writes; conflicting scalar values fail without writing that projection. Results report `applied`, `warned`, `skipped` or `failed`, counts, changed/created/missing/stale entries, warnings and errors. Dry runs report evaluated changes without a rendered patch.

## Faro recipes

Faro remains unchanged and owns its contexts, authentication, normalization and completeness checks. For equivalent native rows, keep an existing Faro source manifest (with `apiVersion: faro.flanksource.com/v1alpha1`) and invoke it from an exec projection:

```yaml
apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: faro-catalog
spec:
  source:
    query:
      exec:
        script: >-
          faro projection run native-source.yaml
          --name catalog-systems --context {{shellQuote .context.name}}
          --json --no-progress
        select: $[0].items
        passthroughEnv: [XDG_CONFIG_HOME]
```

For a complete runnable catalog recipe, save this as `native-source.yaml` and run `query projections run projections.yaml --context staging --json`:

```yaml
apiVersion: faro.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: catalog-systems
spec:
  source:
    query:
      configs:
        configTypes: [Kubernetes::Cluster]
        limit: 500
```

Other existing Faro source manifests can be consumed with the same exec shape, changing `--name` to the native manifest's name. For simpler source-only recipes these existing CLI commands produce the following JSON shapes:

| Data | Exec script | Select | Notes |
| --- | --- | --- | --- |
| Catalog | `faro catalog list --full --json --no-progress` | omit | Array; list limits apply, `config` may remain a string |
| Identity/access | `faro access users export --principal-types users,groups --json --no-progress` | `$.entries` | Export envelope; classification differs from native projection rules |
| Changes | `faro catalog change search 'type=PermissionChange' --json --no-progress` | omit | Compact array; use native projection run for full native rows |
| Insights | `faro catalog insights search 'severity=high' --full --limit 0 --json --no-progress` | omit | Full array with unlimited pagination |

Put the script and select in the complete source-only exec manifest above. Pass `--context {{shellQuote .context.name}}` in each Faro script to select a Faro context explicitly. Selecting `$[0].items` extracts rows; Faro's envelope warnings/context are not merged into the outer result. Use the native command directly when that metadata is required.

The `examples/` directory contains runnable `exec-once.yaml` and `exec-per-target.yaml` with their target, an HTTP manifest, and a complete `faro.yaml` containing all four CLI recipes. Run `query projections verify examples/` from this package directory to check every example offline. Faro recipes make live queries only when explicitly run.

## Go package

```go
docs, err := projections.Load(projections.LoadOptions{Paths: []string{"projections.yaml"}})
if err != nil {
    return err
}
if _, err := projections.Verify(docs); err != nil {
    return err
}
results, err := projections.Apply(ctx, docs, projections.Options{
    Context: map[string]any{"name": "staging"},
    DryRun: true,
})
```

Import `github.com/flanksource/commons-db/projections` and use a commons-db `context.Context`. `Load`, `Verify`, `Run` and `Apply` return typed results; the library does not parse flags or print output. Preserve returned Apply results even when the aggregate error is non-nil, since they describe projections that succeeded and failed.
