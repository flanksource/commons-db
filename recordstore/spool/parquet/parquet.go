// Package parquet is the spool's parquet codec: importing it registers the
// "parquet" format, for producers exporting large batches in columnar form.
//
// The codec writes the rows spool.Publish has normalized, whose values are
// strings, booleans and numbers only — a time or a structured value is
// already its text — so each column is a nullable string, boolean, int64 or
// double. Numbers come back as json.Number, as the ndjson codecs return them.
package parquet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	parquetfile "github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
)

// Format is the spool format the codec registers.
const Format = "parquet"

func init() { spool.Register(Format, Codec{}) }

// Codec writes and reads a data file as one parquet file.
type Codec struct{}

// columnType is the parquet type a column's values share.
type columnType int

const (
	typeNone columnType = iota
	typeString
	typeBool
	typeInt
	typeFloat
)

var arrowTypes = map[columnType]arrow.DataType{
	typeNone: arrow.BinaryTypes.String, typeString: arrow.BinaryTypes.String, typeBool: arrow.FixedWidthTypes.Boolean,
	typeInt: arrow.PrimitiveTypes.Int64, typeFloat: arrow.PrimitiveTypes.Float64,
}

// Encode writes rows as one parquet file whose columns are every key the
// rows hold, sorted, typed by the values they hold.
func (Codec) Encode(w io.Writer, rows []recordstore.Row) error {
	types, err := columnTypes(rows)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	slices.Sort(names)
	fields := make([]arrow.Field, len(names))
	for index, name := range names {
		fields[index] = arrow.Field{Name: name, Type: arrowTypes[types[name]], Nullable: true}
	}
	schema := arrow.NewSchema(fields, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for _, row := range rows {
		for index, name := range names {
			appendValue(builder.Field(index), types[name], row[name])
		}
	}
	record := builder.NewRecordBatch()
	defer record.Release()
	table := array.NewTableFromRecords(schema, []arrow.RecordBatch{record})
	defer table.Release()
	return pqarrow.WriteTable(table, w, max(int64(len(rows)), 1), parquetfile.NewWriterProperties(), pqarrow.DefaultWriterProps())
}

// columnTypes is the type of every column the rows hold: a column mixing
// whole and fractional numbers is fractional, one with only nulls a string,
// and any other mix is refused.
func columnTypes(rows []recordstore.Row) (map[string]columnType, error) {
	types := map[string]columnType{}
	for index, row := range rows {
		for name, value := range row {
			found, err := typeOf(value)
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", index, name, err)
			}
			current, seen := types[name]
			switch {
			case !seen || current == typeNone:
				types[name] = found
			case found == typeNone || found == current:
			case (current == typeInt && found == typeFloat) || (current == typeFloat && found == typeInt):
				types[name] = typeFloat
			default:
				return nil, fmt.Errorf("row %d column %q holds a %T, where earlier rows hold another type", index, name, value)
			}
		}
	}
	return types, nil
}

func typeOf(value any) (columnType, error) {
	switch typed := value.(type) {
	case nil:
		return typeNone, nil
	case string, json.RawMessage:
		return typeString, nil
	case bool:
		return typeBool, nil
	case int, int8, int16, int32, int64, uint8, uint16, uint32:
		return typeInt, nil
	case float32, float64:
		return typeFloat, nil
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return typeInt, nil
		}
		return typeFloat, nil
	default:
		return typeNone, fmt.Errorf("a %T has no parquet type; normalize the row first", value)
	}
}

func appendValue(builder array.Builder, column columnType, value any) {
	if value == nil {
		builder.AppendNull()
		return
	}
	switch column {
	case typeString, typeNone:
		text, ok := value.(string)
		if !ok {
			text = string(value.(json.RawMessage))
		}
		builder.(*array.StringBuilder).Append(text)
	case typeBool:
		builder.(*array.BooleanBuilder).Append(value.(bool))
	case typeInt:
		builder.(*array.Int64Builder).Append(asInt(value))
	case typeFloat:
		builder.(*array.Float64Builder).Append(asFloat(value))
	}
}

func asInt(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case json.Number:
		whole, _ := typed.Int64()
		return whole
	default:
		return value.(int64)
	}
}

func asFloat(value any) float64 {
	switch typed := value.(type) {
	case float32:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		fraction, _ := typed.Float64()
		return fraction
	default:
		return float64(asInt(value))
	}
}

// Decode reads a parquet file, calling fn with each row in order: its
// non-null values, numbers as json.Number.
func (Codec) Decode(r io.Reader, fn func(recordstore.Row) error) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(data), parquetfile.NewReaderProperties(memory.DefaultAllocator),
		pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		return fmt.Errorf("read parquet: %w", err)
	}
	defer table.Release()
	reader := array.NewTableReader(table, max(table.NumRows(), 1))
	defer reader.Release()
	for reader.Next() {
		record := reader.RecordBatch()
		for index := range int(record.NumRows()) {
			row := recordstore.Row{}
			for column, field := range record.Schema().Fields() {
				value, err := valueAt(record.Column(column), index)
				if err != nil {
					return fmt.Errorf("column %q: %w", field.Name, err)
				}
				if value != nil {
					row[field.Name] = value
				}
			}
			if err := fn(row); err != nil {
				return err
			}
		}
	}
	return reader.Err()
}

func valueAt(column arrow.Array, index int) (any, error) {
	if column.IsNull(index) {
		return nil, nil
	}
	switch typed := column.(type) {
	case *array.String:
		return typed.Value(index), nil
	case *array.Boolean:
		return typed.Value(index), nil
	case *array.Int64:
		return json.Number(strconv.FormatInt(typed.Value(index), 10)), nil
	case *array.Float64:
		return json.Number(strconv.FormatFloat(typed.Value(index), 'g', -1, 64)), nil
	default:
		return nil, fmt.Errorf("a %s column is not one the codec writes", column.DataType())
	}
}
