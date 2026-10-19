# Module release preparation

The root, `recordstore`, `tracing`, and `cmd/query` modules share a release version. The Auto Release workflow aligns every sibling requirement before publishing the root tag and the three module tags on the same commit with one atomic push.

Run these commands from the repository root to prepare and verify a release locally:

```sh
export GOWORK=off
go test ./.github/scripts/release-modules
go run ./.github/scripts/release-modules v0.1.60
make tidy
make tidy-check
go run ./.github/scripts/release-modules --check v0.1.60
git diff --check
```

Replace `v0.1.60` with the intended release version. Preparation edits sibling requirements in the three nested `go.mod` files. `make tidy` regenerates module metadata; `make tidy-check` checks that it has stabilized. The final `--check` verifies that all sibling requirements still match the release version and never writes files.

These commands use the checkout's existing sibling replacements because the new tags do not exist yet. They verify release preparation; standalone dependency resolution must also be checked against the published modules after release.

The workflow supports a manual run and always checks out `main`. Publishing remains a workflow step; the commands above do not create tags or push commits.
