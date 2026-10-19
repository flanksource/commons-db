package sqlserver

import (
	"fmt"
	"strconv"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/sqlglot-go/sqlglot"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
)

// Dialect is the sqlglot dialect the driver parses T-SQL with.
const Dialect = "tsql"

// SpecialType is a SQL Server type with no generic Atlas counterpart: money,
// smallmoney, xml, sql_variant and rowversion.
type SpecialType struct {
	schema.Type
	T string
}

// maxSize marks a (max) string or binary size.
const maxSize = -1

const (
	defaultDecimalPrecision = 18
	defaultTimeDigits       = 7
	floatBits               = 53
	realBits                = 24
)

type typeFamily int

const (
	familyFixed typeFamily = iota
	familyInteger
	familyDecimal
	familyFloat
	familyString
	familyBinary
	familyTime
	familySpecial
)

// families lists every type the driver manages. Anything else is refused.
var families = map[string]typeFamily{
	"bit": familyFixed, "uniqueidentifier": familyFixed,
	"date": familyFixed, "datetime": familyFixed, "smalldatetime": familyFixed,
	"text": familyFixed, "ntext": familyFixed, "image": familyFixed,
	"tinyint": familyInteger, "smallint": familyInteger, "int": familyInteger, "bigint": familyInteger,
	"decimal": familyDecimal, "numeric": familyDecimal,
	"float": familyFloat, "real": familyFloat,
	"char": familyString, "varchar": familyString, "nchar": familyString, "nvarchar": familyString,
	"binary": familyBinary, "varbinary": familyBinary,
	"time": familyTime, "datetime2": familyTime, "datetimeoffset": familyTime,
	"money": familySpecial, "smallmoney": familySpecial, "xml": familySpecial,
	"sql_variant": familySpecial, "rowversion": familySpecial,
}

// typeSize is a type's name and its sizes, whichever side it was read from.
type typeSize struct {
	name             string
	length           *int
	precision, scale *int
}

// ParseType reads a declared T-SQL data type (`nvarchar(max)`, `decimal(18,2)`)
// through sqlglot into its schema.Type.
func ParseType(s string) (schema.Type, error) {
	name, err := writtenTypeName(s)
	if err != nil {
		return nil, err
	}
	expr, err := sqlglot.ParseDataType(s, Dialect)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: %w", err)
	}
	spec, err := sqlglot.DataTypeSpecOf(expr, Dialect)
	if err != nil {
		return nil, fmt.Errorf("sqlserver: %w", err)
	}
	return specSize(name, spec).schemaType()
}

func specSize(name string, spec sqlglot.DataTypeSpec) typeSize {
	size := typeSize{name: name, length: spec.Length, precision: spec.Precision, scale: spec.Scale}
	if spec.Max {
		size.length = new(maxSize)
	}
	return size
}

// writtenTypeName is the type's name as written. sqlglot normalises some names
// to another dialect's spelling (REAL to FLOAT, NTEXT to TEXT), which SQL Server
// keeps apart, so the name is read off the first token instead.
func writtenTypeName(s string) (string, error) {
	tokens, err := sqlglot.Tokenize(s, Dialect)
	if err != nil {
		return "", fmt.Errorf("sqlserver: data type %q: %w", s, err)
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("sqlserver: data type is empty")
	}
	name := strings.ToLower(tokens[0].Text)
	if name == "timestamp" {
		name = "rowversion"
	}
	return name, nil
}

// CatalogType maps a catalog column to its schema.Type from the raw
// sys.columns widths: bytes for string and binary types (halved for the
// n-types), -1 for (max), precision and scale for decimals and floats, and the
// fractional-second digits as the scale of time types.
func CatalogType(c mssqlcatalog.Column) (schema.Type, error) {
	if c.MaxLength == nil || c.Precision == nil || c.Scale == nil {
		return nil, fmt.Errorf("sqlserver: column %q has no sys.columns width", c.Name)
	}
	size := typeSize{name: strings.ToLower(c.DataType), precision: c.Precision, scale: c.Scale}
	switch family, known := families[size.name]; {
	case size.name == "timestamp":
		size.name = "rowversion"
	case known && family == familyFloat:
		return typeSize{name: floatName(*c.Precision)}.schemaType()
	}
	length := *c.MaxLength
	if length != maxSize && (size.name == "nchar" || size.name == "nvarchar") {
		length /= 2
	}
	size.length = &length
	return size.schemaType()
}

func (s typeSize) schemaType() (schema.Type, error) {
	family, ok := families[s.name]
	if !ok {
		return nil, fmt.Errorf("sqlserver: type %q is not supported", s.name)
	}
	switch family {
	case familyDecimal:
		precision, scale := valueOr(s.precision, defaultDecimalPrecision), valueOr(s.scale, 0)
		return &schema.DecimalType{T: s.name, Precision: precision, Scale: scale}, nil
	case familyFloat:
		return s.floatType()
	case familyString:
		return &schema.StringType{T: s.name, Size: valueOr(s.length, 1)}, nil
	case familyBinary:
		return &schema.BinaryType{T: s.name, Size: new(valueOr(s.length, 1))}, nil
	case familyTime:
		return &schema.TimeType{T: s.name, Precision: new(valueOr(s.scale, defaultTimeDigits))}, nil
	}
	if s.length != nil && s.isDeclared() {
		return nil, fmt.Errorf("sqlserver: type %q takes no size", s.name)
	}
	return fixedType(s.name, family), nil
}

// isDeclared reports whether the sizes came from a declaration rather than
// the catalog, which reports a byte width for every type.
func (s typeSize) isDeclared() bool { return s.precision == nil && s.scale == nil }

func (s typeSize) floatType() (schema.Type, error) {
	if s.name == "real" {
		if s.length != nil || s.precision != nil {
			return nil, fmt.Errorf("sqlserver: type \"real\" takes no size")
		}
		return &schema.FloatType{T: "real", Precision: realBits}, nil
	}
	bits := valueOr(s.precision, floatBits)
	if bits < 1 || bits > floatBits {
		return nil, fmt.Errorf("sqlserver: float precision %d is out of range 1-53", bits)
	}
	if floatName(bits) == "real" {
		return &schema.FloatType{T: "real", Precision: realBits}, nil
	}
	return &schema.FloatType{T: "float", Precision: floatBits}, nil
}

// floatName is the type SQL Server stores float(n) as: real up to 24 bits.
func floatName(bits int) string {
	if bits <= realBits {
		return "real"
	}
	return "float"
}

func fixedType(name string, family typeFamily) schema.Type {
	switch {
	case family == familyInteger:
		return &schema.IntegerType{T: name}
	case family == familySpecial:
		return &SpecialType{T: name}
	case name == "bit":
		return &schema.BoolType{T: name}
	case name == "uniqueidentifier":
		return &schema.UUIDType{T: name}
	case name == "text" || name == "ntext":
		return &schema.StringType{T: name}
	case name == "image":
		return &schema.BinaryType{T: name}
	}
	return &schema.TimeType{T: name}
}

// FormatType writes a schema.Type in its canonical T-SQL spelling, so two
// spellings of one type format the same.
func FormatType(t schema.Type) (string, error) {
	switch t := t.(type) {
	case *schema.BoolType, *schema.IntegerType, *schema.UUIDType, *SpecialType:
		return fixedName(t), nil
	case *schema.DecimalType:
		return fmt.Sprintf("%s(%d,%d)", t.T, t.Precision, t.Scale), nil
	case *schema.FloatType:
		return t.T, nil
	case *schema.StringType:
		return sized(t.T, t.Size, families[t.T] == familyString), nil
	case *schema.BinaryType:
		return sized(t.T, valueOr(t.Size, 0), families[t.T] == familyBinary), nil
	case *schema.TimeType:
		if t.Precision != nil && families[t.T] == familyTime {
			return fmt.Sprintf("%s(%d)", t.T, *t.Precision), nil
		}
		return t.T, nil
	}
	return "", fmt.Errorf("sqlserver: unsupported type %T", t)
}

func fixedName(t schema.Type) string {
	switch t := t.(type) {
	case *schema.BoolType:
		return t.T
	case *schema.IntegerType:
		return t.T
	case *schema.UUIDType:
		return t.T
	case *SpecialType:
		return t.T
	}
	panic(fmt.Sprintf("sqlserver: %T is not a fixed type", t))
}

func sized(name string, size int, hasSize bool) string {
	switch {
	case !hasSize:
		return name
	case size == maxSize:
		return name + "(max)"
	}
	return name + "(" + strconv.Itoa(size) + ")"
}

func valueOr(v *int, fallback int) int {
	if v == nil {
		return fallback
	}
	return *v
}
