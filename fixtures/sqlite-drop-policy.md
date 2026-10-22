---
cwd: $GIT_ROOT_DIR
codeBlocks: [bash]
---

# Shared SQLite drop policy acceptance

Run from the commons-db checkout with the sibling UIR checkout available. PostgreSQL cases use the standard local embedded test server. Build UIR separately with `make build` before CLI smoke validation against a copied database.

| Name | Command | Timeout | Exit Code | CEL Validation |
| --- | --- | --- | --- | --- |
| Shared migration and SQLite consumers | go test -v ./migrate/... ./db/sqlitetable ./recordstore/sqlite -timeout=5m | 10m | 0 | exitCode == 0 && stdout.contains("PASS") && !stdout.contains("FAIL") |
| UIR schema upgrades | go -C ../uir test -v ./storage -ginkgo.focus='UirDB' -ginkgo.focus='Source revision Git path migration' -ginkgo.focus='H64a' -ginkgo.focus='two-layer fingerprint cutover' -ginkgo.no-color -timeout=5m | 10m | 0 | exitCode == 0 && stdout.contains("Passed") && !stdout.contains("Ran 0") |
| UIR Git source publication | go -C ../uir test -v ./indexer -ginkgo.focus='Git-backed external publication' -ginkgo.no-color -timeout=5m | 10m | 0 | exitCode == 0 && stdout.contains("Passed") && !stdout.contains("Ran 0") |
