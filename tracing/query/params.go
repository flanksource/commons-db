package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbquery "github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/datetime"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/query/providers/sqlite"
)

// Params choose what a capture reads, a saved profile or a provider read in
// place, and the window and pace it reads at.
type Params struct {
	// Profile names a saved query profile, resolved through the host's
	// ProfileResolver.
	Profile string `json:"profile,omitempty" clicky:"title=Profile,desc=Saved query profile to read,order=0"`
	// Provider reads a commons-db query provider in place of a saved profile.
	Provider *Provider `json:"provider,omitempty" clicky:"title=Provider,order=1"`
	// Params are the profile's declared params by name.
	Params map[string]any `json:"params,omitempty" clicky:"title=Params,desc=The profile's params by name,order=2"`
	// From and To bound the window read, as a time or date math such as
	// now-1h. To is empty for now; a window needs a From.
	From string `json:"from,omitempty" clicky:"title=From,desc=Start of the window; a time or date math such as now-1h,order=3"`
	To   string `json:"to,omitempty" clicky:"title=To,desc=End of the window; empty is now,order=4"`
	// Every re-reads the window from the last row kept up to now, at this
	// interval, until the capture stops.
	Every string `json:"every,omitempty" clicky:"title=Every,desc=Re-read the rows written since the last read at this interval; e.g. 15s,order=5"`
	// UntilStopped reads once, when the capture stops, over the whole capture.
	UntilStopped bool `json:"untilStopped,omitempty" clicky:"title=Until stopped,desc=Read once when the capture stops; up to that moment,order=6"`
	// Limit is the most rows one read keeps; 0 keeps every row.
	Limit int `json:"limit,omitempty" clicky:"title=Limit,desc=Most rows one read keeps; 0 keeps all,order=7"`
}

// Provider is a provider read in place: the commons-db provider type, its
// connection and query, and the typed options of that one provider.
type Provider struct {
	// Type is the registered provider key, such as sql, prometheus or opensearch.
	Type       string `json:"type" clicky:"required,title=Type"`
	Connection string `json:"connection,omitempty" clicky:"title=Connection"`
	Query      string `json:"query,omitempty" clicky:"title=Query"`

	SQL           *sqlite.SQLOptions              `json:"sql,omitempty"`
	Prometheus    *providers.PrometheusOptions    `json:"prometheus,omitempty"`
	OpenSearch    *providers.OpenSearchOptions    `json:"opensearch,omitempty"`
	HTTP          *providers.HTTPOptions          `json:"http,omitempty"`
	PostgREST     *providers.PostgRESTOptions     `json:"postgrest,omitempty"`
	BigQuery      *providers.BigQueryOptions      `json:"bigquery,omitempty"`
	Jaeger        *providers.JaegerOptions        `json:"jaeger,omitempty"`
	OpenTelemetry *providers.OpenTelemetryOptions `json:"opentelemetry,omitempty"`

	CloudWatchMetrics *providers.CloudWatchMetricsOptions `json:"cloudwatchmetrics,omitempty"`
}

// typedOptions is one provider's options field: the provider types it
// configures, whether it is set, and its options decoded and checked from a
// provider config's free-form map.
type typedOptions struct {
	field string
	types []string
	value any
	set   bool
	check func(options map[string]any) error
}

func checked[T interface{ Validate() error }](options map[string]any) error {
	decoded, err := dbquery.DecodeOptions[T](options)
	if err != nil {
		return err
	}
	return decoded.Validate()
}

func (p Provider) typed() []typedOptions {
	return []typedOptions{
		{"sql", []string{"sql", "sqlite", "postgres", "mysql", "sqlserver", "clickhouse"}, p.SQL, p.SQL != nil, checked[sqlite.SQLOptions]},
		{"prometheus", []string{"prometheus"}, p.Prometheus, p.Prometheus != nil, checked[providers.PrometheusOptions]},
		{"opensearch", []string{"opensearch"}, p.OpenSearch, p.OpenSearch != nil, checked[providers.OpenSearchOptions]},
		{"http", []string{"http"}, p.HTTP, p.HTTP != nil, checked[providers.HTTPOptions]},
		{"postgrest", []string{"postgrest"}, p.PostgREST, p.PostgREST != nil, checked[providers.PostgRESTOptions]},
		{"bigquery", []string{"bigquery"}, p.BigQuery, p.BigQuery != nil, checked[providers.BigQueryOptions]},
		{"jaeger", []string{"jaeger"}, p.Jaeger, p.Jaeger != nil, checked[providers.JaegerOptions]},
		{"opentelemetry", []string{"opentelemetry"}, p.OpenTelemetry, p.OpenTelemetry != nil, checked[providers.OpenTelemetryOptions]},
		{"cloudwatchmetrics", []string{"cloudwatchmetrics"}, p.CloudWatchMetrics, p.CloudWatchMetrics != nil, checked[providers.CloudWatchMetricsOptions]},
	}
}

// options finds the options field Type reads, refusing a type the kind does
// not read and options set for any other provider.
func (p Provider) options() (typedOptions, error) {
	var found *typedOptions
	var known []string
	for _, candidate := range p.typed() {
		known = append(known, candidate.types...)
		reads := false
		for _, kind := range candidate.types {
			reads = reads || kind == p.Type
		}
		if reads {
			found = &candidate
		} else if candidate.set {
			return typedOptions{}, fmt.Errorf("provider type %q takes no %s options", p.Type, candidate.field)
		}
	}
	if found == nil {
		return typedOptions{}, fmt.Errorf("provider type %q is not one this kind reads; one of %s", p.Type, strings.Join(known, ", "))
	}
	if p.Type == "sqlite" && p.SQL != nil && p.SQL.Type != "" && p.SQL.Type != "sqlite" {
		return typedOptions{}, fmt.Errorf("provider sqlite always reads sqlite, so sql.type %q is never used; use provider sql", p.SQL.Type)
	}
	return *found, nil
}

// profile is the in-place profile the provider reads.
func (p Provider) profile() (dbquery.Profile, error) {
	typed, err := p.options()
	if err != nil {
		return dbquery.Profile{}, err
	}
	profile := dbquery.Profile{Provider: dbquery.ProviderConfig{Type: p.Type, Connection: p.Connection}, Query: p.Query}
	if typed.set {
		encoded, err := json.Marshal(typed.value)
		if err != nil {
			return dbquery.Profile{}, fmt.Errorf("%s options: %w", typed.field, err)
		}
		if err := json.Unmarshal(encoded, &profile.Provider.Options); err != nil {
			return dbquery.Profile{}, fmt.Errorf("%s options: %w", typed.field, err)
		}
	}
	return profile, nil
}

// checkOptions checks a (windowed) in-place profile's options as its provider
// will read them.
func (p Provider) checkOptions(profile dbquery.Profile) error {
	typed, err := p.options()
	if err != nil {
		return err
	}
	return typed.check(profile.Provider.Options)
}

func (p Params) Validate() error {
	if (p.Profile == "") == (p.Provider == nil) {
		return errors.New("name exactly one of profile or provider: a saved profile, or a provider read in place")
	}
	if p.Limit < 0 {
		return fmt.Errorf("limit %d is negative", p.Limit)
	}
	if _, err := p.every(); err != nil {
		return err
	}
	if _, err := p.window(time.Now()); err != nil {
		return err
	}
	if p.Provider == nil {
		return nil
	}
	profile, err := p.Provider.profile()
	if err != nil {
		return err
	}
	if p.windowed() {
		now := time.Now()
		if profile, _, err = dbquery.WindowProfile(profile, p.Params, dbquery.ReadWindow{Start: now, End: now}); err != nil {
			return err
		}
	}
	return p.Provider.checkOptions(profile)
}

// windowed reports whether a read is bounded by a window: one with a From,
// read every interval, or read at the stop.
func (p Params) windowed() bool { return p.From != "" || p.Every != "" || p.UntilStopped }

func (p Params) every() (time.Duration, error) {
	if p.Every == "" {
		return 0, nil
	}
	if p.UntilStopped {
		return 0, errors.New("every re-reads while the capture runs and untilStopped reads once at its stop; pick one")
	}
	if p.To != "" {
		return 0, errors.New("every follows the rows up to now, so it takes no to")
	}
	every, err := time.ParseDuration(p.Every)
	if err != nil || every <= 0 {
		return 0, fmt.Errorf("every %q is not a positive duration such as 15s", p.Every)
	}
	return every, nil
}

// bounds are a capture's window bounds evaluated at its start: a zero start
// is the capture's start, a zero end is the moment each read runs.
type bounds struct{ start, end time.Time }

func (p Params) window(now time.Time) (bounds, error) {
	var window bounds
	if p.To != "" && p.UntilStopped {
		return window, errors.New("untilStopped ends the window when the capture stops, so it takes no to")
	}
	if p.To != "" && p.From == "" {
		return window, fmt.Errorf("to %q needs a from to start the window", p.To)
	}
	for _, bound := range []struct {
		name, value string
		into        *time.Time
	}{{"from", p.From, &window.start}, {"to", p.To, &window.end}} {
		if bound.value == "" {
			continue
		}
		parsed, err := datetime.Parse(bound.value, now)
		if err != nil {
			return window, fmt.Errorf("%s: %w", bound.name, err)
		}
		*bound.into = parsed.Time.UTC()
	}
	return window, nil
}
