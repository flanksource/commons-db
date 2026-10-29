package metrics

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"time"

	"modernc.org/sqlite"
)

func init() {
	for _, name := range []string{"first", "last"} {
		sqlite.MustRegisterFunction(name, &sqlite.FunctionImpl{NArgs: 2, Deterministic: true, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &edge{last: name == "last"}, nil
		}})
	}
}

type edgeValue struct {
	value driver.Value
	time  time.Time
}
type edge struct {
	last   bool
	values []edgeValue
}

func (e *edge) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[1] == nil {
		return nil
	}
	text, ok := args[1].(string)
	if !ok {
		return fmt.Errorf("first/last timestamp must be RFC3339")
	}
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return err
	}
	e.values = append(e.values, edgeValue{value: args[0], time: t})
	return nil
}
func (e *edge) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[1] == nil {
		return nil
	}
	if len(e.values) == 0 {
		return fmt.Errorf("first/last window invariant violated")
	}
	e.values = e.values[1:]
	return nil
}
func (e *edge) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if len(e.values) == 0 {
		return nil, nil
	}
	chosen := e.values[0]
	for _, v := range e.values[1:] {
		if e.last && v.time.After(chosen.time) || !e.last && v.time.Before(chosen.time) {
			chosen = v
		} else if v.time.Equal(chosen.time) && !reflect.DeepEqual(v.value, chosen.value) {
			return nil, fmt.Errorf("first/last requires unambiguous values at tied timestamps")
		}
	}
	return chosen.value, nil
}
func (e *edge) Final(*sqlite.FunctionContext) {}
