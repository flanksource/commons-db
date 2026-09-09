package inspector

import "time"

// Table represents a database table or view
type Table struct {
	Schema     string
	Name       string
	Type       string // "table" or "view"
	ViewDef    string // SQL definition for views
	CreateDate *time.Time
}

// Column represents a table column
type Column struct {
	TableName        string
	ColumnName       string
	DataType         string
	IsNullable       bool
	DefaultValue     *string
	Position         int
	IsAutoIncrement  bool
	IsPrimaryKey     bool
	IsUnique         bool
	MaxLength        *int // For varchar, char
	NumericScale     *int // For decimal, numeric
	NumericPrecision *int // For decimal, numeric
	Comment          *string
	EnumValues       []string // For enum types
}

// Index represents a table index
type Index struct {
	TableName string
	IndexName string
	IsUnique  bool
	IsPrimary bool
	Columns   []string // Ordered list of column names
	Type      string   // btree, hash, gin, gist, etc.
	Condition string   // WHERE clause for partial indexes
}

// ForeignKey represents a foreign key constraint
type ForeignKey struct {
	TableName      string
	ColumnName     string
	RefTableName   string
	RefColumnName  string
	ConstraintName string
}

// StoredProc represents a stored procedure or function
type StoredProc struct {
	Schema     string
	Name       string
	Type       string // "procedure" or "function"
	SQL        string // Complete SQL body/definition
	ReturnType string // For functions: the SQL return type (e.g. "int", "TABLE"); empty for procedures
	CreateDate *time.Time
}

// ProcParam represents a stored procedure parameter
type ProcParam struct {
	ProcName  string
	ParamName string
	DataType  string
	Position  int
}

// Trigger represents a database trigger attached to a table.
//
// Event is one or more trigger events, comma-joined when a single trigger
// fires on multiple DML actions (e.g. "INSERT,UPDATE"). Timing is "AFTER"
// or "INSTEAD OF"; SQL Server "BEFORE" triggers map to "AFTER" since the
// engine does not support true BEFORE semantics.
type Trigger struct {
	Schema     string
	Name       string
	TableName  string
	Event      string
	Timing     string
	IsDisabled bool
	SQL        string // CREATE TRIGGER body from OBJECT_DEFINITION
	CreateDate *time.Time
}
