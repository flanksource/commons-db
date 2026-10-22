// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"

type Rows = native.Rows

type Queryer = native.Queryer

func FromDB(db SQLQueryer) Queryer { return native.FromDB(db) }

type SQLQueryer = native.SQLQueryer

type Scope = native.Scope
