// Package dbtarget preserves the commons-db API; implementations live in sqlglot-go.
package dbtarget

import native "github.com/flanksource/sqlglot-go/database/target"

type Dialect = native.Dialect

const Postgres = native.Postgres

const SQLite = native.SQLite

const SQLServer = native.SQLServer

type Target = native.Target

var Parse = native.Parse
