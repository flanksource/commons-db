package sqlite

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

// storage classes in SQLite's cross-type order: NULL < INTEGER/REAL < TEXT < BLOB.
const (
	classNull = iota
	classNumeric
	classText
	classBlob
)

// CompareSQLite orders two scanned values the way SQLite's BINARY collation
// does, so rows read from several files merge in the order one file would have
// returned them. Integers compare exactly against each other and against reals;
// a float64 detour would tie distinct int64 keys above 2^53. A time.Time is
// TEXT in storage and compares chronologically against another time, and as its
// RFC3339Nano text against a string.
func CompareSQLite(a, b any) (int, error) {
	classA, err := storageClass(a)
	if err != nil {
		return 0, err
	}
	classB, err := storageClass(b)
	if err != nil {
		return 0, err
	}
	if classA != classB {
		return compareInts(int64(classA), int64(classB)), nil
	}
	switch classA {
	case classNull:
		return 0, nil
	case classNumeric:
		return compareNumeric(a, b), nil
	case classText:
		return compareText(a, b), nil
	default:
		return bytes.Compare(a.([]byte), b.([]byte)), nil
	}
}

// compareOrderKeys orders two rows' keys under order: nulls last in both
// directions, as buildPagedSQL renders ORDER BY and the keyset predicate.
func compareOrderKeys(a, b []any, order query.Order) (int, error) {
	for index, by := range order {
		x, y := a[index], b[index]
		switch {
		case x == nil && y == nil:
			continue
		case x == nil:
			return 1, nil
		case y == nil:
			return -1, nil
		}
		compared, err := CompareSQLite(x, y)
		if err != nil {
			return 0, fmt.Errorf("order column %q: %w", by.Column, err)
		}
		if by.Desc {
			compared = -compared
		}
		if compared != 0 {
			return compared, nil
		}
	}
	return 0, nil
}

func storageClass(value any) (int, error) {
	switch value.(type) {
	case nil:
		return classNull, nil
	case int, int8, int16, int32, int64, float32, float64, bool:
		return classNumeric, nil
	case string, time.Time:
		return classText, nil
	case []byte:
		return classBlob, nil
	default:
		return 0, fmt.Errorf("cannot order a %T value as a SQLite value", value)
	}
}

func compareNumeric(a, b any) int {
	intA, isIntA := asInt64(a)
	intB, isIntB := asInt64(b)
	switch {
	case isIntA && isIntB:
		return compareInts(intA, intB)
	case isIntA:
		return compareIntFloat(intA, asFloat64(b))
	case isIntB:
		return -compareIntFloat(intB, asFloat64(a))
	}
	x, y := asFloat64(a), asFloat64(b)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// compareIntFloat compares exactly: the float's integer part is compared as an
// int64 and only its fraction breaks a tie.
func compareIntFloat(i int64, f float64) int {
	switch {
	case f >= math.MaxInt64:
		return -1
	case f < math.MinInt64:
		return 1
	}
	whole := math.Floor(f)
	if compared := compareInts(i, int64(whole)); compared != 0 {
		return compared
	}
	if f > whole {
		return -1
	}
	return 0
}

func compareText(a, b any) int {
	timeA, isTimeA := a.(time.Time)
	timeB, isTimeB := b.(time.Time)
	if isTimeA && isTimeB {
		return timeA.Compare(timeB)
	}
	return strings.Compare(asText(a), asText(b))
}

func asText(value any) string {
	if t, ok := value.(time.Time); ok {
		return t.Format(time.RFC3339Nano)
	}
	return value.(string)
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func asFloat64(value any) float64 {
	if typed, ok := value.(float32); ok {
		return float64(typed)
	}
	return value.(float64)
}

func compareInts(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
