// Package connection preserves the commons-db API; implementations live in sqlglot-go.
package connection

import native "github.com/flanksource/sqlglot-go/database/capabilities"
import "context"
import "database/sql"

type BackendCapability = native.BackendCapability

const BackendCapabilityArrayFilters = native.BackendCapabilityArrayFilters

type BackendCapabilityStatus = native.BackendCapabilityStatus

type BackendCapabilities = native.BackendCapabilities

func ProbeBackendCapabilities(ctx context.Context, db *sql.DB, backend string) (BackendCapabilities, error) {
	return native.ProbeBackendCapabilities(ctx, db, backend)
}
