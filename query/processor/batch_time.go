package processor

import (
	"fmt"
	"time"

	"github.com/flanksource/commons/utils"

	"github.com/flanksource/commons-db/query"
)

// batchTimestamp reads and buckets the grouping timestamp. A row without a
// usable value is an error: silently bucketing it to the zero time would merge
// every such row into one batch.
func batchTimestamp(row query.Row, column string, window time.Duration) (time.Time, error) {
	value, ok := row[column]
	if !ok || value == nil {
		return time.Time{}, fmt.Errorf("timestamp column %q is missing", column)
	}
	parsed, err := parseBatchTime(value)
	if err != nil {
		return time.Time{}, err
	}
	if window > 0 {
		return parsed.Truncate(window), nil
	}
	return parsed, nil
}

// epochFloors bound the epoch units a bare number can be, largest unit first: a
// value at or above the floor is that unit. The floors sit around 1973 in each
// unit, so any realistic log timestamp lands on the right one.
var epochFloors = []struct {
	floor int64
	unit  time.Duration
}{
	{1e17, time.Nanosecond},
	{1e14, time.Microsecond},
	{1e11, time.Millisecond},
	{0, time.Second},
}

// parseBatchTime normalizes the shapes providers actually return: time.Time from
// the loki and prometheus providers, RFC3339 strings from the opentelemetry
// parser, and bare epochs from raw document stores.
func parseBatchTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed, nil
	case *time.Time:
		if typed == nil {
			return time.Time{}, fmt.Errorf("timestamp is nil")
		}
		return *typed, nil
	case string:
		if parsed := utils.ParseTime(typed); parsed != nil {
			return *parsed, nil
		}
		return time.Time{}, fmt.Errorf("timestamp %q is not a recognized time", typed)
	case int, int32, int64, float32, float64:
		return epochTime(value)
	default:
		return time.Time{}, fmt.Errorf("timestamp %v (%T) is not a time", value, value)
	}
}

func epochTime(value any) (time.Time, error) {
	var epoch int64
	switch typed := value.(type) {
	case int:
		epoch = int64(typed)
	case int32:
		epoch = int64(typed)
	case int64:
		epoch = typed
	case float32:
		epoch = int64(typed)
	case float64:
		epoch = int64(typed)
	}
	if epoch <= 0 {
		return time.Time{}, fmt.Errorf("timestamp %d is not a positive epoch", epoch)
	}
	for _, candidate := range epochFloors {
		if epoch >= candidate.floor {
			return time.Unix(0, epoch*int64(candidate.unit)), nil
		}
	}
	return time.Unix(epoch, 0), nil
}
