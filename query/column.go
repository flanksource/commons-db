package query

import (
	"fmt"
	"slices"

	"github.com/flanksource/clicky/api"
)

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
	ColumnTypeUUID      ColumnType = "uuid"
	ColumnTypeKeyValue  ColumnType = "key_value"
	ColumnTypeKeyValues ColumnType = "key_values"
	ColumnTypeJSON      ColumnType = "json"
)

// ColumnTypeValues returns every type an author may declare, for the profile
// schema's enum.
func ColumnTypeValues() []string {
	return []string{
		string(ColumnTypeString), string(ColumnTypeNumber), string(ColumnTypeBoolean),
		string(ColumnTypeDateTime), string(ColumnTypeDuration), string(ColumnTypeBytes),
		string(ColumnTypeStatus), string(ColumnTypeHealth), string(ColumnTypeUUID),
		string(ColumnTypeKeyValue), string(ColumnTypeKeyValues), string(ColumnTypeJSON),
	}
}

// Enumerable reports whether asking the backend to list this column's distinct
// values would answer anything. An identifier is unique per row, so its value
// list is a list of the rows — a scan of the whole result to offer twenty
// values nobody recognizes. Such a column is typed into, not picked from.
func (t ColumnType) Enumerable() bool {
	return t != ColumnTypeUUID
}

// ColumnKind enables semantic table behavior beyond value formatting.
type ColumnKind string

const (
	ColumnKindTimestamp ColumnKind = "timestamp"
	ColumnKindTags      ColumnKind = "tags"
	ColumnKindStatus    ColumnKind = "status"
)

// ColumnDef declares one output column of a Profile.
type ColumnDef struct {
	// Name is the public row key and default header label.
	Name string `json:"name" yaml:"name"`

	// Source is the provider row key copied into Name. The original key is
	// removed after all column expressions have been evaluated.
	Source string `json:"source,omitempty" yaml:"source,omitempty"`

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

	// Unit is an optional display unit (e.g. "ms", "bytes", "percentunit").
	Unit string `json:"unit,omitempty" yaml:"unit,omitempty"`

	// Width is an optional max display width in characters.
	Width int `json:"width,omitempty" yaml:"width,omitempty"`

	// CEL is an optional expression computing the cell value from the row.
	// The row is exposed as `row` in the CEL environment.
	CEL string `json:"cel,omitempty" yaml:"cel,omitempty"`

	// JSONPath is an optional path computing the cell value. It is rooted at the
	// row, or at Source when that is set — and a Source holding JSON as a string
	// is parsed first, so an encoded and a native column read the same way. A
	// path matching nothing yields nil: on a scan, a row that lacks the field is
	// ordinary rather than exceptional. Alternative to CEL, not a companion.
	//
	// Source names the root here; it does not rename. Several columns read one
	// JSON column, and that column stays in the row rather than being consumed
	// by whichever of them ran first.
	JSONPath string `json:"jsonpath,omitempty" yaml:"jsonpath,omitempty"`

	// Filter overrides how this column is filtered at the backend. Direct
	// columns, simple row/span CEL lookups and literal-key-chain JSONPaths infer
	// the field automatically and Type infers the control; computed CEL, and a
	// JSONPath that selects rather than addresses, need an explicit field to be
	// filterable at all.
	Filter *ColumnFilterDef `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Hidden excludes the column from rendered output while keeping it available
	// to later column and style CEL expressions.
	Hidden bool `json:"hidden,omitempty" yaml:"hidden,omitempty"`

	// Style is an optional CEL expression returning this cell's presentation
	// classes (e.g. `level == "ERROR" ? "text-red-500" : ""`). It reads the row
	// the same way CEL does and affects rendering only: the row itself is
	// unchanged, so an export carries the value without the styling.
	Style string `json:"style,omitempty" yaml:"style,omitempty"`
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

// Validate rejects unsupported display metadata before a profile executes.
func (c ColumnDef) Validate() error {
	if c.Source != "" && c.CEL != "" {
		return fmt.Errorf("column %q cannot set both source and cel", c.Name)
	}
	if c.CEL != "" && c.JSONPath != "" {
		return fmt.Errorf("column %q cannot set both cel and jsonpath", c.Name)
	}
	if c.JSONPath != "" {
		if _, err := compileColumnJSONPath(c); err != nil {
			return err
		}
	}
	if c.Filter != nil {
		if err := c.Filter.Validate(c.Name); err != nil {
			return err
		}
	}
	if c.Format != "" && !slices.Contains(api.ColumnFormatValues(), c.Format) {
		return fmt.Errorf("column %q format %q is unsupported", c.Name, c.Format)
	}
	if c.Unit == "" {
		return nil
	}
	if !slices.Contains(api.ColumnUnitValues(), c.Unit) {
		return fmt.Errorf("column %q unit %q is unsupported", c.Name, c.Unit)
	}
	switch c.Type {
	case ColumnTypeNumber, ColumnTypeDuration, ColumnTypeBytes:
		return nil
	default:
		return fmt.Errorf("column %q unit requires type number, duration, or bytes", c.Name)
	}
}
