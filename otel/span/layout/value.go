package layout

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query/esdsl"
)

func (f Field) validateOrdering() error {
	if len(f.Values) > 0 {
		return fmt.Errorf("source enum %s has no reversible canonical ordering", f.Source)
	}
	return nil
}

func (f Field) value(value any, canonical string, direction Direction) (any, error) {
	if len(f.Values) > 0 {
		if direction == ToSource {
			mapped, ok := f.Values[fmt.Sprint(value)]
			if !ok {
				return nil, fmt.Errorf("status %q is not representable by %s", value, f.Source)
			}
			return mapped, nil
		}
		for name, mapped := range f.Values {
			if fmt.Sprint(mapped) == fmt.Sprint(value) {
				return name, nil
			}
		}
		return nil, fmt.Errorf("source status %v has no canonical value", value)
	}
	if canonical == "duration_ms" && f.Unit != "" && f.Unit != "ms" {
		return scale(value, f.Unit, direction)
	}
	if canonical == "timestamp" && (f.Type == "date" || f.Type == "date_nanos") {
		if direction == ToSource {
			return esdsl.EncodeTimeValue(value, esdsl.Search{TimeField: f.Source}, &esdsl.TimeFieldMapping{Type: f.Type, Format: f.Format, Now: time.Now().UTC()})
		}
		if _, err := strconv.ParseInt(fmt.Sprint(value), 10, 64); err != nil {
			return value, nil
		}
		unit := "ms"
		if strings.Contains(f.Format, "epoch_second") && !strings.Contains(f.Format, "epoch_millis") {
			unit = "s"
		}
		return (Field{Source: f.Source, Type: "long", Unit: unit}).value(value, canonical, direction)
	}
	if canonical == "timestamp" && f.Unit != "" {
		if direction == ToSource {
			format := map[string]esdsl.TimeFieldFormat{"s": esdsl.TimeFieldFormatEpochSecond, "ms": esdsl.TimeFieldFormatEpochMillis, "us": esdsl.TimeFieldFormatEpochMicros, "ns": esdsl.TimeFieldFormatEpochNanos}[f.Unit]
			if format == "" {
				return nil, fmt.Errorf("unknown timestamp unit %q", f.Unit)
			}
			return esdsl.EncodeTimeValue(value, esdsl.Search{TimeField: f.Source, TimeFieldFormat: format}, &esdsl.TimeFieldMapping{Type: f.Type, Now: time.Now().UTC()})
		}
		n, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid source timestamp: %w", err)
		}
		factor := map[string]int64{"s": 1, "ms": 1000, "us": 1000000, "ns": 1000000000}[f.Unit]
		if factor == 0 {
			return nil, fmt.Errorf("unknown timestamp unit %q", f.Unit)
		}
		return time.Unix(n/factor, (n%factor)*(1000000000/factor)).UTC().Format(time.RFC3339Nano), nil
	}
	if direction == ToCanonical {
		return f.number(value)
	}
	return value, nil
}

func (f Field) number(value any) (any, error) {
	raw := fmt.Sprint(value)
	if number, ok := value.(float64); ok {
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("%s requires a finite number", f.Source)
		}
		if (f.Type == "long" || f.Type == "integer" || f.Type == "short" || f.Type == "byte" || f.Type == "unsigned_long") && math.Abs(number) > 9007199254740991 {
			return nil, fmt.Errorf("%s integer lost precision in the browser sample", f.Source)
		}
		raw = strconv.FormatFloat(number, 'f', -1, 64)
	}
	switch f.Type {
	case "long", "integer", "short", "byte":
		return strconv.ParseInt(raw, 10, 64)
	case "unsigned_long":
		return strconv.ParseUint(raw, 10, 64)
	case "double", "float", "half_float", "scaled_float":
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("%s requires a finite number", f.Source)
		}
		return number, nil
	default:
		return value, nil
	}
}

func scale(value any, unit string, direction Direction) (any, error) {
	nanos, ok := map[string]int64{"s": 1000000000, "ms": 1000000, "us": 1000, "ns": 1}[unit]
	if !ok {
		return nil, fmt.Errorf("unknown duration unit %q", unit)
	}
	number, ok := new(big.Rat).SetString(fmt.Sprint(value))
	if !ok {
		return nil, fmt.Errorf("duration must be numeric, received %v", value)
	}
	factor := big.NewRat(1000000, nanos)
	if direction == ToCanonical {
		factor.Inv(factor)
	}
	number.Mul(number, factor)
	decimal := number.FloatString(30)
	roundtrip, _ := new(big.Rat).SetString(decimal)
	if number.Cmp(roundtrip) != 0 {
		return nil, fmt.Errorf("duration conversion would lose precision")
	}
	decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	if decimal == "" || decimal == "-0" {
		decimal = "0"
	}
	return json.Number(decimal), nil
}
