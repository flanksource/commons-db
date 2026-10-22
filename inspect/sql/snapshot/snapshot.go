// Package snapshot preserves the commons-db API; implementations live in sqlglot-go.
package snapshot

import native "github.com/flanksource/sqlglot-go/database/inspect/snapshot"

type Options = native.Options

type Result = native.Result

var Inspect = native.Inspect
