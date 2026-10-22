// Package inspector preserves the commons-db API; implementations live in sqlglot-go.
package inspector

import native "github.com/flanksource/sqlglot-go/database/inspect/inspector"
import "database/sql"

type Table = native.Table

type Column = native.Column

type Index = native.Index

type ForeignKey = native.ForeignKey

type StoredProc = native.StoredProc

type ProcParam = native.ProcParam

type Trigger = native.Trigger

type Inspector = native.Inspector

func NewInspector(db *sql.DB, driver string, bounds *Bounds) (Inspector, error) {
	return native.NewInspector(db, driver, bounds)
}
