package projections

import (
	"math"
	"slices"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"github.com/flanksource/gomplate/v4"
)

var projectionStatistics = []gomplate.CelFunction{
	{
		Name: "percentile",
		Overloads: []gomplate.CelOverload{{
			ID: "projection_percentile", Member: true,
			Args:   []*cel.Type{cel.ListType(cel.DynType), cel.DynType},
			Result: cel.DoubleType,
		}},
		Impl: func(_ string, args ...ref.Val) ref.Val {
			q, ok := sampleNumber(args[1])
			if !ok {
				return types.NewErr("percentile requires a numeric q, got %s", args[1].Type())
			}
			if !(q > 0 && q <= 100) {
				return types.NewErr("percentile requires q in (0, 100], got %v", q)
			}
			samples, err := orderedSamples("percentile", args[0])
			if err != nil {
				return err
			}
			return types.Double(nearestRank(samples, q))
		},
	},
	{
		Name: "stats",
		Overloads: []gomplate.CelOverload{{
			ID: "projection_stats", Member: true,
			Args:   []*cel.Type{cel.ListType(cel.DynType)},
			Result: cel.MapType(cel.StringType, cel.DynType),
		}},
		Impl: func(_ string, args ...ref.Val) ref.Val {
			samples, err := orderedSamples("stats", args[0])
			if err != nil {
				return err
			}
			var sum float64
			for _, sample := range samples {
				sum += sample
			}
			count := float64(len(samples))
			average := sum / count
			if math.IsInf(sum, 0) {
				scale := max(-samples[0], samples[len(samples)-1])
				sum = 0
				for _, sample := range samples {
					sum += sample / scale
				}
				average = (sum / count) * scale
			}
			return types.NewStringInterfaceMap(types.DefaultTypeAdapter, map[string]any{
				"min": samples[0], "avg": average, "max": samples[len(samples)-1],
				"p50": nearestRank(samples, 50), "p90": nearestRank(samples, 90),
				"p95": nearestRank(samples, 95), "p99": nearestRank(samples, 99),
				"samples": int64(len(samples)),
			})
		},
	},
}

func orderedSamples(name string, value ref.Val) ([]float64, ref.Val) {
	list, ok := value.(traits.Lister)
	if !ok {
		return nil, types.NewErr("%s requires a list of samples, got %s", name, value.Type())
	}
	samples := make([]float64, 0, int(list.Size().(types.Int)))
	for iterator := list.Iterator(); iterator.HasNext() == types.True; {
		item := iterator.Next()
		if types.IsUnknownOrError(item) {
			return nil, item
		}
		sample, ok := sampleNumber(item)
		if !ok {
			return nil, types.NewErr("%s requires numeric samples, got %s", name, item.Type())
		}
		if math.IsNaN(sample) || math.IsInf(sample, 0) {
			return nil, types.NewErr("%s requires finite samples, got %v", name, sample)
		}
		samples = append(samples, sample)
	}
	if len(samples) == 0 {
		return nil, types.NewErr("%s requires at least one sample", name)
	}
	slices.Sort(samples)
	return samples, nil
}

func sampleNumber(value ref.Val) (float64, bool) {
	switch number := value.(type) {
	case types.Int:
		return float64(number), true
	case types.Uint:
		return float64(number), true
	case types.Double:
		return float64(number), true
	default:
		return 0, false
	}
}

func nearestRank(samples []float64, q float64) float64 {
	rank := int(math.Ceil(q * float64(len(samples)) / 100))
	return samples[max(rank-1, 0)]
}
