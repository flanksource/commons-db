// Package migrate preserves the commons-db API; implementations live in sqlglot-go.
package migrate

import native "github.com/flanksource/sqlglot-go/database/migrate"
import "io/fs"

type SchemaProvisioner = native.SchemaProvisioner

func NewProvisioner(schemaFS fs.FS, opts ...Option) *SchemaProvisioner {
	return native.NewProvisioner(schemaFS, opts...)
}
