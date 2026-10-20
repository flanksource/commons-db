# Git Server-Timing

Git operations contribute to the Clicky timing collector carried by their context. The CLI path remains usable without a collector. HTTP handlers should install `github.com/flanksource/clicky/rpc/http.TimingMiddleware` before calling Git services and pass the request context throughout.

```go
ctx, timings := rpchttp.WithTimings(context.Background())
repo, err := git.NewRepository(git.RepositoryOptions{URL: "file:///path/to/rules.git"})
if err != nil {
    return err
}
refs, err := repo.LsRemote(ctx)
if err != nil {
    return err
}
fmt.Println(len(refs.Branches), timings.Header())
```

The example imports `context`, `fmt`, `github.com/flanksource/commons-db/git` and `rpchttp` from `github.com/flanksource/clicky/rpc/http`.

The `git` metric counts `ops`, `ls_remote`, `commands` and `errors`. Its duration is the aggregate time spent listing remote refs and running Git subprocesses. A subprocess returning an error still contributes its duration. The process-wide Git-version prerequisite is included in the first command's time. Command arguments and repository identifiers are not emitted.

Go-git remote calls carry HTTP trace hooks through their context, exposing DNS, connection, TLS, first-byte and connection-reuse metrics. Provider REST calls use the shared HTTP client's exchange/body metrics. Git subprocess networking is included in `git`, without inventing HTTP measurements for an external process. HTTP connection phases overlap Git operation time and must not be added to it.

The instrumentation does not change ref freshness, mirror caching, authentication, TLS, fetch policy or transport registration. See the commons HTTP Server-Timing documentation for connection and body metric definitions.
