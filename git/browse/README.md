# Git browsing

`browse.Service` resolves configured connections and repositories for read-only UI discovery. It lists redacted connections, repositories, refs, commit pages, files, pipelines and executions. `ReadFile` requires a full commit SHA and a repository-relative regular-file path; file discovery returns that SHA alongside the paths. Credentials stay in the server-side resolver.

Construct a service with `browse.Options`: `Catalog` lists configured connection models, `Resolve` hydrates the selected connection's credential references, and `CacheDir` stores shared bare Git mirrors. Provider connections accept only repositories returned by that provider's catalog. A plain Git connection pins its repository and rejects a separate repo parameter.

Mount `httpapi.API.Handler(operation)` under your application's prefix. `httpapi.New` requires `Authorize` and `Service` callbacks; authorization runs before input parsing or credential resolution. Supported operations are exposed by `httpapi.Operations`. Call `httpapi.AddOpenAPI(spec, prefix)` to document the same contract.

```go
api, err := httpapi.New(httpapi.Options{
    Authorize: authorizeGitRead,
    Service: resolveGitBrowser,
})
if err != nil {
    return err
}
for _, operation := range httpapi.Operations {
    mux.HandleFunc("GET /api/git/"+operation, api.Handler(operation))
}
```

`connection` names a configured connection. `repo` identifies the selected provider repository. `ref` selects a branch, tag or full SHA. `extension` may repeat to restrict file discovery, for example `.xlsx`. Commit, pipeline and execution endpoints accept `limit` (1–100) and opaque `cursor` values returned as `nextCursor`. Execution requests additionally require `pipeline`, with optional `branch`. Commit cursors pin the first page's SHA so later pages remain stable if a branch moves.

Azure DevOps build definitions/builds and GitHub Actions workflows/runs implement `git.PipelineProvider`, an optional capability obtained through `git.PipelineAPI`. Other providers return `git.ErrPipelinesUnsupported`. Execution metadata retains provider status/result, source branch, full SHA, author, timestamp and stable run ID. Browsing never starts a pipeline or modifies repository state.

Run the focused provider, service and HTTP contracts with `go test ./git/...`.
