// Package inspector preserves the commons-db API; implementations live in sqlglot-go.
package inspector

import native "github.com/flanksource/sqlglot-go/database/inspect/inspector"

type Table = native.Table

type Column = native.Column

type Index = native.Index

type ForeignKey = native.ForeignKey

type StoredProc = native.StoredProc

type ProcParam = native.ProcParam

type Trigger = native.Trigger

type Inspector = native.Inspector

var NewInspector = native.NewInspector
