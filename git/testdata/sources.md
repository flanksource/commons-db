---
cwd: $GIT_ROOT_DIR
---

# Prepared local sources

| Name | Command | Exit Code | CEL Validation |
| --- | --- | --- | --- |
| Source selection and isolation | go test ./git -count=1 -ginkgo.focus='Prepared sources' -ginkgo.fail-on-empty -ginkgo.no-color | 0 | stdout.contains("ok") |
