package schedules

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/cmd/query/profiles"
)

// Kind is the task kind every scheduled run carries, and the key the runner is
// registered under with clicky's scheduler.
const Kind = "schedule"

// Schedule is a recurring query or reconciliation, optionally rendered to a
// report and delivered. It is the stored spec; clicky's task.Schedule is the
// timing half derived from it.
type Schedule struct {
	Name      string `json:"name" yaml:"name" clicky:"title=Name,order=1"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty" clicky:"title=Namespace,order=2"`

	// Cron is a five-field spec or a descriptor ("@daily", "@every 30m").
	Cron     string `json:"cron" yaml:"cron" clicky:"type=cron,title=Schedule,order=3"`
	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty" clicky:"title=Timezone,order=4"`
	Enabled  bool   `json:"enabled" yaml:"enabled" clicky:"title=Enabled,order=5"`

	// Overlap and CatchUp are per-schedule because there is no answer that is
	// right for both a cheap probe and an expensive report.
	Overlap string   `json:"overlap,omitempty" yaml:"overlap,omitempty" clicky:"title=On overlap,order=6"`
	CatchUp string   `json:"catchUp,omitempty" yaml:"catchUp,omitempty" clicky:"title=On missed runs,order=7"`
	Timeout Duration `json:"timeout,omitempty" yaml:"timeout,omitempty" clicky:"title=Timeout,order=8"`

	// Exactly one of Query and Reconcile says what the schedule runs.
	Query     *QuerySpec     `json:"query,omitempty" yaml:"query,omitempty"`
	Reconcile *ReconcileSpec `json:"reconcile,omitempty" yaml:"reconcile,omitempty"`

	Report  *ReportSpec    `json:"report,omitempty" yaml:"report,omitempty"`
	Deliver []DeliverySpec `json:"deliver,omitempty" yaml:"deliver,omitempty"`

	Labels map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Owner  string            `json:"owner,omitempty" yaml:"owner,omitempty"`
}

// QuerySpec runs one profile. Params are the profile's own filter params, the
// same key=value pairs `query profiles run --param` takes.
type QuerySpec struct {
	Profile string            `json:"profile" yaml:"profile"`
	Params  map[string]string `json:"params,omitempty" yaml:"params,omitempty"`

	// Limit caps the rows read. Zero uses the profile's own export ceiling —
	// a scheduled report has no user watching to stop a runaway read.
	Limit int `json:"limit,omitempty" yaml:"limit,omitempty"`
}

// ReconcileSpec joins two profiles. It embeds the same flags the manual
// `reconcile` action takes, so a scheduled reconciliation and a hand-run one are
// configured identically rather than by two drifting sets of fields.
type ReconcileSpec struct {
	Profile                 string `json:"profile" yaml:"profile"`
	profiles.ReconcileFlags `yaml:",inline"`
}

// ReportSpec renders the result. Formats are the ones the profile export
// already serves — csv, json, yaml, markdown, html, excel, pdf — plus
// facet-html and facet-pdf, which render a TSX template through facet.
type ReportSpec struct {
	Title  string `json:"title,omitempty" yaml:"title,omitempty"`
	Format string `json:"format" yaml:"format"`

	// Template selects the TSX entry file for the facet formats. Empty uses the
	// embedded default.
	Template  string            `json:"template,omitempty" yaml:"template,omitempty"`
	Variables map[string]string `json:"variables,omitempty" yaml:"variables,omitempty"`

	Facet *FacetSpec `json:"facet,omitempty" yaml:"facet,omitempty"`
}

// FacetSpec points at the facet render service. Connection or URL selects it;
// when neither is set the local facet binary is used instead.
type FacetSpec struct {
	Connection   string   `json:"connection,omitempty" yaml:"connection,omitempty"`
	URL          string   `json:"url,omitempty" yaml:"url,omitempty"`
	Timeout      Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Header       string   `json:"header,omitempty" yaml:"header,omitempty"`
	Footer       string   `json:"footer,omitempty" yaml:"footer,omitempty"`
	TimestampURL string   `json:"timestampUrl,omitempty" yaml:"timestampUrl,omitempty"`
}

// DeliverySpec sends the result to one channel, named by connection.
type DeliverySpec struct {
	Connection string `json:"connection" yaml:"connection"`

	Title   string `json:"title,omitempty" yaml:"title,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`

	// Attach embeds the rendered report in the message. Only SMTP carries
	// attachments; every other channel gets a link to the stored artifact, and
	// asking for an attachment on one is refused rather than silently dropped.
	Attach bool `json:"attach,omitempty" yaml:"attach,omitempty"`

	// Properties are per-channel overrides, using the same `channel.key` prefix
	// convention the notification senders read.
	Properties map[string]string `json:"properties,omitempty" yaml:"properties,omitempty"`
}

// Duration is a time.Duration that marshals as a human string ("30m") rather
// than a nanosecond count, because these values are typed by people.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	if d == 0 {
		return []byte(`""`), nil
	}
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// GetID and GetName make a Schedule a clicky EntityItem. The name is the id:
// schedules are addressed by the name their author gave them, like profiles.
func (s Schedule) GetID() string   { return s.Name }
func (s Schedule) GetName() string { return s.Name }

// Validate reports the first reason the schedule could not run. It is enforced
// on save so a schedule that can never fire is refused at the point someone
// writes it, not discovered as silence.
func (s Schedule) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("schedule name is required")
	}
	if s.Query == nil && s.Reconcile == nil {
		return fmt.Errorf("schedule %q: one of query or reconcile is required", s.Name)
	}
	if s.Query != nil && s.Reconcile != nil {
		return fmt.Errorf("schedule %q: query and reconcile are mutually exclusive", s.Name)
	}
	if s.Query != nil && strings.TrimSpace(s.Query.Profile) == "" {
		return fmt.Errorf("schedule %q: query.profile is required", s.Name)
	}
	if s.Reconcile != nil && strings.TrimSpace(s.Reconcile.Profile) == "" {
		return fmt.Errorf("schedule %q: reconcile.profile is required", s.Name)
	}
	if s.Report != nil {
		if err := s.Report.validate(s.Name); err != nil {
			return err
		}
	}
	for _, delivery := range s.Deliver {
		if err := delivery.validate(s.Name, s.Report); err != nil {
			return err
		}
	}
	// Timing is clicky's to judge, so ask it rather than restating the rules.
	return s.Timing().Validate()
}

func (r ReportSpec) validate(schedule string) error {
	if !supportedReportFormat(r.Format) {
		return fmt.Errorf("schedule %q: unsupported report format %q", schedule, r.Format)
	}
	return nil
}

func (d DeliverySpec) validate(schedule string, report *ReportSpec) error {
	if strings.TrimSpace(d.Connection) == "" {
		return fmt.Errorf("schedule %q: delivery connection is required", schedule)
	}
	if d.Attach && report == nil {
		return fmt.Errorf("schedule %q: delivery %q asks to attach a report the schedule does not produce",
			schedule, d.Connection)
	}
	return nil
}

// Timing projects the stored spec onto clicky's schedule, which owns cron
// parsing, timezone resolution and the overlap/catch-up policies.
func (s Schedule) Timing() task.Schedule {
	labels := map[string]string{}
	for k, v := range s.Labels {
		labels[k] = v
	}
	return task.Schedule{
		Name:     s.Name,
		Kind:     Kind,
		Labels:   labels,
		Owner:    s.Owner,
		Cron:     s.Cron,
		Timezone: s.Timezone,
		Enabled:  s.Enabled,
		Timeout:  time.Duration(s.Timeout),
		Overlap:  task.OverlapPolicy(s.Overlap),
		CatchUp:  task.CatchUpPolicy(s.CatchUp),
	}
}
