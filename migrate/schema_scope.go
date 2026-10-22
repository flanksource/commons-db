// Package migrate preserves the commons-db API; implementations live in sqlglot-go.
package migrate

import native "github.com/flanksource/sqlglot-go/database/migrate"

func ValidateSchemaName(name string) error { return native.ValidateSchemaName(name) }

func ConnectionForSchema(connection, schemaName string) (string, error) {
	return native.ConnectionForSchema(connection, schemaName)
}
