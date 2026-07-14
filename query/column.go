package query

// ColumnType is the semantic type of a column. It drives default formatting in
// the render layer (see render.go) and the clicky-ui contract.
//
// The set mirrors duty/view.ColumnType so view specs port cleanly; it is
// expanded with format/filter/badge metadata in Phase 2.
type ColumnType string

const (
	ColumnTypeString    ColumnType = "string"
	ColumnTypeNumber    ColumnType = "number"
	ColumnTypeBoolean   ColumnType = "boolean"
	ColumnTypeDateTime  ColumnType = "datetime"
	ColumnTypeDuration  ColumnType = "duration"
	ColumnTypeBytes     ColumnType = "bytes"
	ColumnTypeStatus    ColumnType = "status"
	ColumnTypeHealth    ColumnType = "health"
	ColumnTypeKeyValue  ColumnType = "key_value"
	ColumnTypeKeyValues ColumnType = "key_values"
	ColumnTypeJSON      ColumnType = "json"
)

// ColumnKind enables semantic table behavior beyond value formatting.
type ColumnKind string

const (
	ColumnKindTimestamp ColumnKind = "timestamp"
	ColumnKindTags      ColumnKind = "tags"
	ColumnKindStatus    ColumnKind = "status"
)

// ColumnDef declares one output column of a Profile.
type ColumnDef struct {
	// Name is the row key this column reads from and the default header label.
	Name string `json:"name" yaml:"name"`

	// Label overrides the column header. Defaults to a prettified Name.
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// Type is the semantic type used for formatting. Defaults to string.
	Type ColumnType `json:"type,omitempty" yaml:"type,omitempty"`

	// Kind enables specialized table behavior. In particular, timestamp marks
	// the column used by the table's date-range control.
	Kind ColumnKind `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Format overrides the clicky format string (e.g. "date", "bytes",
	// "duration", "currency"). When empty it is derived from Type.
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	// Unit is an optional display unit (e.g. "ms", "MiB").
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`

	// Width is an optional max display width in characters.
	Width int `json:"width,omitempty" yaml:"width,omitempty"`

	// CEL is an optional expression computing the cell value from the row.
	// The row is exposed as `row` in the CEL environment.
	CEL string `json:"cel,omitempty" yaml:"cel,omitempty"`

	// Hidden excludes the column from rendered output while keeping it available
	// to CEL and processors.
	Hidden bool `json:"hidden,omitempty" yaml:"hidden,omitempty"`
}

// clickyFormat returns the clicky format string for the column: the explicit
// Format override when set, otherwise the default derived from Type.
func (c ColumnDef) clickyFormat() string {
	if c.Format != "" {
		return c.Format
	}
	switch c.Type {
	case ColumnTypeDateTime:
		return "date"
	case ColumnTypeDuration:
		return "duration"
	case ColumnTypeBytes:
		return "bytes"
	case ColumnTypeNumber:
		return "float"
	default:
		return ""
	}
}
