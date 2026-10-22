// Package migrate preserves the commons-db API; implementations live in sqlglot-go.
package migrate

import native "github.com/flanksource/sqlglot-go/database/migrate"

func WithRoutines(dir string) Option { return native.WithRoutines(dir) }
