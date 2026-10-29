package metrics

import (
	"database/sql/driver"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/flanksource/commons/duration"
	"modernc.org/sqlite"
)

func init() {
	for _, name := range []string{"percentile", "percentile_cont", "median"} {
		n := int32(2)
		if name == "median" {
			n = 1
		}
		sqlite.MustRegisterFunction(name, &sqlite.FunctionImpl{NArgs: n, Deterministic: true, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &percentile{name: name}, nil }})
	}
	sqlite.MustRegisterDeterministicScalarFunction("time_bucket", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		width, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("bucket width must be a duration")
		}
		d, err := duration.ParseDuration(width)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid bucket width %q", width)
		}
		text, ok := args[1].(string)
		if !ok {
			return nil, fmt.Errorf("bucket time must be a timestamp")
		}
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return nil, err
		}
		ns := t.UnixNano()
		w := int64(d)
		q := ns / w
		if ns < 0 && ns%w != 0 {
			q--
		}
		return formatTime(time.Unix(0, q*w)), nil
	})
}

type percentile struct {
	name   string
	values []float64
	p      float64
	set    bool
}

func numeric(v driver.Value) (float64, error) {
	switch v := v.(type) {
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v, nil
		}
	case int64:
		return float64(v), nil
	}
	return 0, fmt.Errorf("percentile requires finite numeric input, got %v", v)
}
func (p *percentile) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	rank := 50.0
	if p.name != "median" {
		var err error
		rank, err = numeric(args[1])
		if err != nil {
			return err
		}
		if p.name == "percentile_cont" {
			rank *= 100
		}
	}
	if rank < 0 || rank > 100 || p.set && rank != p.p {
		return fmt.Errorf("percentile must be constant in range 0..100")
	}
	p.p = rank
	p.set = true
	if args[0] == nil {
		return nil
	}
	value, err := numeric(args[0])
	if err != nil {
		return err
	}
	p.values = append(p.values, value)
	return nil
}
func (p *percentile) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	v, err := numeric(args[0])
	if err != nil {
		return err
	}
	for i, item := range p.values {
		if item == v {
			p.values = append(p.values[:i], p.values[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("percentile window invariant violated")
}
func (p *percentile) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if len(p.values) == 0 {
		return nil, nil
	}
	sort.Float64s(p.values)
	pos := p.p / 100 * float64(len(p.values)-1)
	lo := int(pos)
	hi := int(math.Ceil(pos))
	return p.values[lo] + (p.values[hi]-p.values[lo])*(pos-float64(lo)), nil
}
func (p *percentile) Final(*sqlite.FunctionContext) {}
