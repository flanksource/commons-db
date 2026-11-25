package gcp

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"time"
)

// BigQueryField is one column of a result schema.
type BigQueryField struct {
	Name string `json:"name"`
	// Type is the BigQuery type name, e.g. STRING, INTEGER, RECORD.
	Type string `json:"type"`
	// Mode is NULLABLE, REQUIRED or REPEATED.
	Mode string `json:"mode"`
	// Fields are the sub-fields of a RECORD.
	Fields []*BigQueryField `json:"fields,omitempty"`
}

// BigQuerySchema is the ordered column list of a result set.
type BigQuerySchema []*BigQueryField

func decodeRow(schema BigQuerySchema, row bigQueryRow) ([]any, error) {
	if len(row.F) != len(schema) {
		return nil, fmt.Errorf("gcp: bigquery row has %d cells, schema has %d fields", len(row.F), len(schema))
	}
	out := make([]any, len(schema))
	for i, field := range schema {
		value, err := decodeCell(field, row.F[i].V)
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

// decodeCell converts the wire form of a cell (null, string, array or object)
// into the Go value for the field's type.
func decodeCell(field *BigQueryField, raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	if field.Mode == "REPEATED" {
		return decodeRepeated(field, raw)
	}
	return decodeSingle(field, raw)
}

func decodeRepeated(field *BigQueryField, raw any) (any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("gcp: bigquery field %s: repeated value is %T, want array", field.Name, raw)
	}
	element := *field
	element.Mode = ""
	out := make([]any, len(items))
	for i, item := range items {
		cell, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("gcp: bigquery field %s: repeated element is %T, want object", field.Name, item)
		}
		value, err := decodeSingle(&element, cell["v"])
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

func decodeSingle(field *BigQueryField, raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch field.Type {
	case "RECORD", "STRUCT":
		return decodeRecord(field, raw)
	default:
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("gcp: bigquery field %s (%s): value is %T, want string", field.Name, field.Type, raw)
		}
		value, err := decodeScalar(field.Type, text)
		if err != nil {
			return nil, fmt.Errorf("gcp: bigquery field %s (%s): %w", field.Name, field.Type, err)
		}
		return value, nil
	}
}

func decodeRecord(field *BigQueryField, raw any) (any, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gcp: bigquery field %s: record value is %T, want object", field.Name, raw)
	}
	cells, _ := object["f"].([]any)
	if len(cells) != len(field.Fields) {
		return nil, fmt.Errorf("gcp: bigquery field %s: record has %d cells, schema has %d fields", field.Name, len(cells), len(field.Fields))
	}
	out := make([]any, len(cells))
	for i, sub := range field.Fields {
		cell, _ := cells[i].(map[string]any)
		value, err := decodeCell(sub, cell["v"])
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

func decodeScalar(typeName, text string) (any, error) {
	switch typeName {
	case "STRING", "JSON", "GEOGRAPHY", "INTERVAL", "DATE", "TIME", "DATETIME":
		return text, nil
	case "INTEGER", "INT64":
		return strconv.ParseInt(text, 10, 64)
	case "FLOAT", "FLOAT64":
		return strconv.ParseFloat(text, 64)
	case "BOOLEAN", "BOOL":
		return strconv.ParseBool(text)
	case "TIMESTAMP":
		return parseTimestamp(text)
	case "BYTES":
		return base64.StdEncoding.DecodeString(text)
	case "NUMERIC", "BIGNUMERIC", "DECIMAL", "BIGDECIMAL":
		rat, ok := new(big.Rat).SetString(text)
		if !ok {
			return nil, fmt.Errorf("invalid decimal %q", text)
		}
		return rat, nil
	default:
		return nil, fmt.Errorf("unsupported type %q", typeName)
	}
}

// parseTimestamp reads BigQuery's seconds-since-epoch form ("1.356998400E9").
func parseTimestamp(text string) (time.Time, error) {
	seconds, ok := new(big.Rat).SetString(text)
	if !ok {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", text)
	}
	nanos := new(big.Int).Quo(
		new(big.Int).Mul(seconds.Num(), big.NewInt(int64(time.Second))),
		seconds.Denom(),
	)
	return time.Unix(0, nanos.Int64()).UTC(), nil
}
