// Package migrate preserves the commons-db API; implementations live in sqlglot-go.
package migrate

import native "github.com/flanksource/sqlglot-go/database/migrate"
import "context"
import "database/sql"
import "io/fs"
import "github.com/zclconf/go-cty/cty"

type Option = native.Option

type SchemaProvisioner = native.SchemaProvisioner

func NewProvisioner(schemaFS fs.FS, opts ...Option) *SchemaProvisioner {
	return native.NewProvisioner(schemaFS, opts...)
}

func WithRoutines(dir string) Option { return native.WithRoutines(dir) }

func WithRebuilds() Option { return native.WithRebuilds() }

func WithDir(dir string) Option { return native.WithDir(dir) }

func WithName(name string) Option { return native.WithName(name) }

func WithSchema(name string) Option { return native.WithSchema(name) }

func WithVariables(input map[string]cty.Value) Option { return native.WithVariables(input) }

func WithExclude(patterns ...string) Option { return native.WithExclude(patterns...) }

func WithDrops() Option { return native.WithDrops() }

func WithDB(db *sql.DB) Option { return native.WithDB(db) }

func Apply(ctx context.Context, connection string, schemaFS fs.FS, opts ...Option) (returnErr error) {
	return native.Apply(ctx, connection, schemaFS, opts...)
}
