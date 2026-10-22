// Package sqlserver preserves the commons-db API; implementations live in sqlglot-go.
package sqlserver

import native "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"

type Codec = native.Codec

var TypeRegistry = native.TypeRegistry

var MarshalHCL = native.MarshalHCL

var EvalHCL = native.EvalHCL

var EvalHCLBytes = native.EvalHCLBytes
