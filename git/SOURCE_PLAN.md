# Prepared local source API

Status: implementation complete; focused verification complete in the linked workspace. Standalone dependency and package-wide lint gates have existing failures listed below.

The shared seam is `commons-db/git`, consumed independently by UIR and Gavel. This change implements the local source-preparation API; lint adapters, UIR indexing and CLI wiring remain consumer work.

1. Add typed source options, source metadata, changed paths and idempotent ownership cleanup on `Cache.PrepareSource`.
2. Resolve local commits and endpoint/merge-base ranges. Capture staged contents with a copied index, using private tree objects and read-only access to existing local objects.
3. Materialize complete raw trees with executable bits and contained relative symlinks. Reject unmerged indexes, gitlinks, escaping symlinks, missing objects and invalid selectors explicitly.
4. Return live checkout changes without allocating or owning the checkout. Include nonignored untracked paths and retain deletions and literal filenames.
5. Verify with Ginkgo integration tests against disposable repositories, document the public contract and attach the runnable source fixture to the tracked work. Do not commit, publish or change dependency versions as part of this implementation.

Implemented files: `source.go`, `source_repository.go`, `source_index.go`, `source_changes.go`, `source_materialize.go`, `source_test.go`, `source_edge_test.go`, `SOURCES.md`, `testdata/sources.md`, this plan and the package documentation entry in `doc.go`.

Verification: focused source tests and the full `go test ./git` suite passed in the linked workspace. Gavel golangci-lint with `GOTOOLCHAIN=go1.26.1` reported three pre-existing ST1005 violations in unrelated `git/pipelines_azure.go`; no violations referenced the new source files. The default Go 1.27 toolchain exceeds the installed linter's Go 1.26 build. `make build` and `make lint` are unavailable targets in this repository. `GOWORK=off go test ./git` cannot compile existing `client_options.go`, `remote.go` and `rest.go` because the pinned commons dependency lacks `TransportPool`, `WithClientTimings` and `TransportOptions`. These failures are outside source preparation and were left intact.
