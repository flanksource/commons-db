package types

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unsafe"

	jsoniter "github.com/json-iterator/go"
	jsontime "github.com/liamylian/jsontime/v2/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const (
	SQLServerType = "sqlserver"
	PostgresType  = "postgres"
	SqliteType    = "sqlite"
	MysqlType     = "mysql"
	Text          = "TEXT"
	JSONType      = "JSON"
	JSONBType     = "JSONB"
	NVarcharType  = "NVARCHAR(MAX)"
)

const PostgresTimestampFormat = "2006-01-02T15:04:05.999999"

func init() {
	jsontime.AddTimeFormatAlias("postgres_timestamp", PostgresTimestampFormat)
	jsoniter.RegisterTypeDecoderFunc("time.Duration", func(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
		t, err := time.ParseDuration(iter.ReadString())
		if err != nil {
			iter.Error = err
			return
		}
		*((*time.Duration)(ptr)) = t
	})
}

// JSON defined JSON data type, need to implements driver.Valuer, sql.Scanner interface
type JSON json.RawMessage

// Value return json value, implement driver.Valuer interface
func (j JSON) Value() (driver.Value, error) {
	return GenericStructValue(j, true)
}

// Scan scan value into Jsonb, implements sql.Scanner interface
func (j *JSON) Scan(value any) error {
	return GenericStructScan(&j, value)
}

// MarshalJSON to output non base64 encoded []byte
func (j JSON) MarshalJSON() ([]byte, error) {
	return json.RawMessage(j).MarshalJSON()
}

// UnmarshalJSON to deserialize []byte
func (j *JSON) UnmarshalJSON(b []byte) error {
	result := json.RawMessage{}
	err := result.UnmarshalJSON(b)
	*j = JSON(result)
	return err
}

func (j JSON) String() string {
	return string(j)
}

// GormDataType gorm common data type
func (JSON) GormDataType() string {
	return JSONType
}

// GormDBDataType gorm db data type
func (JSON) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return JSONGormDBDataType(db.Dialector.Name())
}

func (js JSON) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	if len(js) == 0 {
		return gorm.Expr("NULL")
	}

	data, _ := js.MarshalJSON()
	return gorm.Expr("?", string(data))
}

// JSONQueryExpression json query expression, implements clause.Expression interface to use as querier
type JSONQueryExpression struct {
	column      string
	keys        []string
	hasKeys     bool
	equals      bool
	equalsValue any
}

// JSONQuery query column as json
func JSONQuery(column string) *JSONQueryExpression {
	return &JSONQueryExpression{column: column}
}

// HasKey returns clause.Expression
func (jsonQuery *JSONQueryExpression) HasKey(keys ...string) *JSONQueryExpression {
	jsonQuery.keys = keys
	jsonQuery.hasKeys = true
	return jsonQuery
}

// Keys returns clause.Expression
func (jsonQuery *JSONQueryExpression) Equals(value any, keys ...string) *JSONQueryExpression {
	jsonQuery.keys = keys
	jsonQuery.equals = true
	jsonQuery.equalsValue = value
	return jsonQuery
}

// Build implements clause.Expression
func (jsonQuery *JSONQueryExpression) Build(builder clause.Builder) {
	if stmt, ok := builder.(*gorm.Statement); ok {
		switch stmt.Dialector.Name() {
		case MysqlType, SqliteType:
			switch {
			case jsonQuery.hasKeys:
				if len(jsonQuery.keys) > 0 {
					_, _ = builder.WriteString("JSON_EXTRACT(" + stmt.Quote(jsonQuery.column) + ",")
					builder.AddVar(stmt, "$."+strings.Join(jsonQuery.keys, "."))
					_, _ = builder.WriteString(") IS NOT NULL")
				}
			case jsonQuery.equals:
				if len(jsonQuery.keys) > 0 {
					_, _ = builder.WriteString("JSON_EXTRACT(" + stmt.Quote(jsonQuery.column) + ",")
					builder.AddVar(stmt, "$."+strings.Join(jsonQuery.keys, "."))
					_, _ = builder.WriteString(") = ")
					if _, ok := jsonQuery.equalsValue.(bool); ok {
						_, _ = builder.WriteString(fmt.Sprint(jsonQuery.equalsValue))
					} else {
						stmt.AddVar(builder, jsonQuery.equalsValue)
					}
				}
			}
		case PostgresType:
			switch {
			case jsonQuery.hasKeys:
				if len(jsonQuery.keys) > 0 {
					stmt.WriteQuoted(jsonQuery.column)
					_, _ = stmt.WriteString("::jsonb")
					for _, key := range jsonQuery.keys[0 : len(jsonQuery.keys)-1] {
						_, _ = stmt.WriteString(" -> ")
						stmt.AddVar(builder, key)
					}

					_, _ = stmt.WriteString(" ? ")
					stmt.AddVar(builder, jsonQuery.keys[len(jsonQuery.keys)-1])
				}
			case jsonQuery.equals:
				if len(jsonQuery.keys) > 0 {
					_, _ = builder.WriteString(fmt.Sprintf("json_extract_path_text(%v::json,", stmt.Quote(jsonQuery.column)))

					for idx, key := range jsonQuery.keys {
						if idx > 0 {
							_ = builder.WriteByte(',')
						}
						stmt.AddVar(builder, key)
					}
					_, _ = builder.WriteString(") = ")

					if _, ok := jsonQuery.equalsValue.(string); ok {
						stmt.AddVar(builder, jsonQuery.equalsValue)
					} else {
						stmt.AddVar(builder, fmt.Sprint(jsonQuery.equalsValue))
					}
				}
			}
		}
	}
}

// JSONStringMap defiend JSON data type, need to implements driver.Valuer, sql.Scanner interface
type JSONStringMap map[string]string

// Value return json value, implement driver.Valuer interface
func (m JSONStringMap) Value() (driver.Value, error) {
	return GenericStructValue(m, true)
}

// Scan scan value into Jsonb, implements sql.Scanner interface
func (m *JSONStringMap) Scan(val any) error {
	return GenericStructScan(&m, val)
}

// MarshalJSON to output non base64 encoded []byte
func (m JSONStringMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	t := (map[string]string)(m)
	return json.Marshal(t)
}

// UnmarshalJSON to deserialize []byte
func (m *JSONStringMap) UnmarshalJSON(b []byte) error {
	t := map[string]string{}
	err := json.Unmarshal(b, &t)
	*m = JSONStringMap(t)
	return err
}

// GormDataType gorm common data type
func (m JSONStringMap) GormDataType() string {
	return "jsonstringmap"
}

// GormDBDataType gorm db data type
func (JSONStringMap) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return JSONGormDBDataType(db.Dialector.Name())
}

func (jm JSONStringMap) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	data, _ := jm.MarshalJSON()
	return gorm.Expr("?", string(data))
}

func (jm JSONStringMap) ToMapStringAny() map[string]any {
	r := make(map[string]any, len(jm))
	for k, v := range jm {
		r[k] = v
	}

	return r
}

// JSONMap defiend JSON data type, need to implements driver.Valuer, sql.Scanner interface
type JSONMap map[string]any

// Value return json value, implement driver.Valuer interface
func (m JSONMap) Value() (driver.Value, error) {
	return GenericStructValue(m, true)
}

// Scan scan value into Jsonb, implements sql.Scanner interface
func (m *JSONMap) Scan(val any) error {
	return GenericStructScan(&m, val)
}

// MarshalJSON to output non base64 encoded []byte
func (m JSONMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	t := (map[string]any)(m)
	return json.Marshal(t)
}

// UnmarshalJSON to deserialize []byte
func (m *JSONMap) UnmarshalJSON(b []byte) error {
	t := map[string]any{}
	err := json.Unmarshal(b, &t)
	*m = JSONMap(t)
	return err
}

// GormDataType gorm common data type
func (m JSONMap) GormDataType() string {
	return "jsonmap"
}

// GormDBDataType gorm db data type
func (JSONMap) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return JSONGormDBDataType(db.Dialector.Name())
}

func (jm JSONMap) GormValue(ctx context.Context, db *gorm.DB) clause.Expr {
	data, _ := jm.MarshalJSON()
	return gorm.Expr("?", string(data))
}

// GenericStructValue can be set as the Value() func for any json struct
func GenericStructValue[T any](t T, defaultNull bool) (driver.Value, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return b, err
	}
	if defaultNull && string(b) == "{}" {
		return nil, nil
	}
	return string(b), nil
}

// GenericStructScan can be set as the Scan(val) func for any json struct
func GenericStructScan[T any](t *T, val any) error {
	if val == nil {
		t = new(T)
		return nil
	}
	var ba []byte
	switch v := val.(type) {
	case []byte:
		ba = v
	case string:
		ba = []byte(v)
	default:
		return fmt.Errorf("Failed to unmarshal JSONB value: %v", val)
	}
	err := json.Unmarshal(ba, &t)
	return err
}

func JSONGormDBDataType(dialect string) string {
	switch dialect {
	case SqliteType:
		return Text
	case PostgresType:
		return JSONBType
	case SQLServerType:
		return NVarcharType
	}

	return ""
}

func GormValue(t any) clause.Expr {
	data, _ := json.Marshal(t)
	return gorm.Expr("?", string(data))
}
