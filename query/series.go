package query

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	clickymetrics "github.com/flanksource/clicky/metrics"
)

// SeriesSpec projects a profile's rows onto plottable series (Profile.Series).
//
// Time is the timestamp column (default `time`, else `timestamp`, the column a
// PromQL row carries); Value the numeric column (default `value`). Labels are
// the columns that identify a series (default: every other column); Metric
// names a column holding the metric name, always part of the identity. Unit is
// a clicky-ui formatUnit key, and Label a Go template over the series' labels
// (e.g. `{{.pod}}`).
type SeriesSpec struct {
	Time   string   `json:"time,omitempty" yaml:"time,omitempty"`
	Value  string   `json:"value,omitempty" yaml:"value,omitempty"`
	Labels []string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metric string   `json:"metric,omitempty" yaml:"metric,omitempty"`
	Unit   string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Label  string   `json:"label,omitempty" yaml:"label,omitempty"`

	// Source is the id the series answer — what a panel asked for. It is set by
	// the caller, never authored, and defaults to the result's profile name.
	Source string `json:"-" yaml:"-"`
}

// ProjectSeries groups result's rows by label set into series with ascending
// points. A series' ID is `<source>#<k=v,...>` over its sorted labels, or the
// source itself when the result is a single unlabelled series. A row whose
// value is not numeric or whose time does not parse fails the projection: a
// dropped point would draw a line that is not there.
func ProjectSeries(result Result, spec SeriesSpec) ([]clickymetrics.Series, error) {
	if len(result.Rows) == 0 {
		return []clickymetrics.Series{}, nil
	}
	source := spec.Source
	if source == "" {
		source = result.Profile
	}
	spec, err := spec.resolve(result.Rows[0])
	if err != nil {
		return nil, fmt.Errorf("profile %q series: %w", result.Profile, err)
	}
	label, err := template.New("label").Option("missingkey=error").Parse(spec.Label)
	if err != nil {
		return nil, fmt.Errorf("profile %q series label template: %w", result.Profile, err)
	}
	grouped := map[string]*clickymetrics.Series{}
	for i, row := range result.Rows {
		point, labels, err := spec.point(row)
		if err != nil {
			return nil, fmt.Errorf("profile %q row %d: %w", result.Profile, i, err)
		}
		key := canonicalLabels(labels)
		s, ok := grouped[key]
		if !ok {
			s = &clickymetrics.Series{ID: source + "#" + key, Source: source, Labels: labels, Unit: spec.Unit}
			if s.Label, err = renderSeriesLabel(label, spec.Label, labels); err != nil {
				return nil, fmt.Errorf("profile %q: %w", result.Profile, err)
			}
			grouped[key] = s
		}
		s.Points = append(s.Points, point)
	}
	return orderedSeries(grouped, source), nil
}

// resolve fills the spec's defaults from the first row's columns.
func (spec SeriesSpec) resolve(first Row) (SeriesSpec, error) {
	if spec.Time == "" {
		for _, candidate := range []string{"time", "timestamp"} {
			if _, ok := first[candidate]; ok {
				spec.Time = candidate
				break
			}
		}
		if spec.Time == "" {
			return spec, fmt.Errorf("no time column: rows carry neither `time` nor `timestamp`; set series.time")
		}
	}
	if spec.Value == "" {
		spec.Value = "value"
	}
	if len(spec.Labels) == 0 {
		for column := range first {
			if column != spec.Time && column != spec.Value {
				spec.Labels = append(spec.Labels, column)
			}
		}
	}
	if spec.Metric != "" && !slices.Contains(spec.Labels, spec.Metric) {
		spec.Labels = append(spec.Labels, spec.Metric)
	}
	return spec, nil
}

func (spec SeriesSpec) point(row Row) (clickymetrics.Point, map[string]string, error) {
	at, err := seriesTime(row[spec.Time])
	if err != nil {
		return clickymetrics.Point{}, nil, fmt.Errorf("time column %q: %w", spec.Time, err)
	}
	value, ok := seriesValue(row[spec.Value])
	if !ok {
		return clickymetrics.Point{}, nil, fmt.Errorf("value column %q holds %T %v, not a number", spec.Value, row[spec.Value], row[spec.Value])
	}
	labels := map[string]string{}
	for _, column := range spec.Labels {
		if v, ok := row[column]; ok && v != nil {
			labels[column] = fmt.Sprint(v)
		}
	}
	return clickymetrics.Point{At: at, Value: value}, labels, nil
}

func seriesTime(v any) (time.Time, error) {
	switch v := v.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		return t.UTC(), err
	}
	return time.Time{}, fmt.Errorf("%T %v is not a timestamp", v, v)
}

func seriesValue(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func canonicalLabels(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		pairs = append(pairs, k+"="+labels[k])
	}
	return strings.Join(pairs, ",")
}

func renderSeriesLabel(label *template.Template, text string, labels map[string]string) (string, error) {
	if text == "" {
		return "", nil
	}
	var out strings.Builder
	if err := label.Execute(&out, labels); err != nil {
		return "", fmt.Errorf("series label template %q: %w", text, err)
	}
	return out.String(), nil
}

func orderedSeries(grouped map[string]*clickymetrics.Series, source string) []clickymetrics.Series {
	out := make([]clickymetrics.Series, 0, len(grouped))
	for _, key := range slices.Sorted(maps.Keys(grouped)) {
		s := grouped[key]
		sort.SliceStable(s.Points, func(i, j int) bool { return s.Points[i].At.Before(s.Points[j].At) })
		if len(s.Labels) == 0 {
			s.Labels = nil
		}
		out = append(out, *s)
	}
	if len(out) == 1 && out[0].Labels == nil {
		out[0].ID = source
	}
	return out
}
