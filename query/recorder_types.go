package query

// The wire types a devtools console reads. They are split in two on purpose.
//
// ExecutionSummary is what every armed request produces and what a console
// streams: it costs nothing beyond what the execution already recorded, because
// no preview is marshalled and no backend is asked anything. ExecutionDetail is
// the expensive half — bodies, previews, backend instrumentation — and is
// fetched for one record at a time, when someone opens it.
//
// The split is not a compression trick. A console watching a busy server would
// otherwise be pushed megabytes of response bodies for queries nobody looked at.

import (
	"time"

	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
)

// ExecutionSource says what asked for this execution. Surface is the seam the
// request came in on; a record whose Surface is empty means something armed a
// request the recorder does not understand, which is a bug rather than a row.
type ExecutionSource struct {
	Surface string `json:"surface"` // profile | browser | sample | reconcile
	Profile string `json:"profile,omitempty"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	// Query is the client's query string with credential-shaped values blanked,
	// kept so a console can offer "re-run with bodies" without the caller having
	// to reconstruct the request by hand.
	Query string `json:"query,omitempty"`
}

// RecordCounts says what detail exists to fetch, so a badge can be rendered
// without paying for the payload behind it.
type RecordCounts struct {
	Operations  int `json:"operations"`
	HAREntries  int `json:"harEntries"`
	HARDropped  int `json:"harDropped"`
	LogLines    int `json:"logLines"`
	LogDropped  int `json:"logDropped"`
	Probes      int `json:"probes"`
	Inspections int `json:"inspections"`
}

// OperationSummary is one provider operation inside a request: ProviderDiagnostics
// with everything expensive left out.
//
// A request is routinely more than one operation — a profile with context
// sub-queries runs several — and reporting them as one row is what makes "which
// of these three queries was slow" unanswerable today.
type OperationSummary struct {
	Index      int     `json:"index"` // 1-based, in the order they started
	Provider   string  `json:"provider"`
	Connection string  `json:"connection,omitempty"`
	Query      string  `json:"query,omitempty"`
	Method     string  `json:"method,omitempty"`
	URL        string  `json:"url,omitempty"`
	Status     int     `json:"status,omitempty"`
	DurationMS float64 `json:"durationMs"`
	Rows       int     `json:"rows"`
	Pages      int     `json:"pages,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// ExecutionSummary is the cheap half: what ran, against what, how long, and how
// much came back.
type ExecutionSummary struct {
	ID       string `json:"id"`
	Sequence int64  `json:"sequence"` // assigned by the store; the SSE event id

	Source     ExecutionSource `json:"source"`
	StartedAt  time.Time       `json:"startedAt"`
	DurationMS float64         `json:"durationMs"`
	Rows       int             `json:"rows"`
	Status     int             `json:"status,omitempty"`
	Error      string          `json:"error,omitempty"`

	// Level names the capture level this run was armed at. A console showing a
	// record it did not arm needs it to explain why the bodies are missing,
	// rather than implying the request made none.
	Level      string             `json:"level"`
	Operations []OperationSummary `json:"operations,omitempty"`
	Counts     RecordCounts       `json:"counts"`
}

// LogLine is one line a request logged, or one the process logged while no
// request owned it.
//
// Source distinguishes the two because they are not equally trustworthy:
// a request line is captured structurally, with its values intact, while a
// process line is scraped from the writer the logger was already using and has
// been rendered to text by the time it arrives.
type LogLine struct {
	Sequence int64          `json:"sequence"`
	Time     time.Time      `json:"time"`
	Level    string         `json:"level"`
	Source   string         `json:"source"` // request | process
	Logger   string         `json:"logger,omitempty"`
	Event    string         `json:"event,omitempty"` // an observability.Event, when it came from one
	Message  string         `json:"message"`
	Values   map[string]any `json:"values,omitempty"`

	// RecordID and Operation say which execution, and which operation within it,
	// this line belongs to. Operation is 0 when the line is not attributable to
	// one — a line the request logged before any provider ran.
	RecordID  string `json:"recordId,omitempty"`
	Operation int    `json:"operation,omitempty"`
}

// CardinalityProbe records a column-cardinality question and the filter kind its
// answer chose.
//
// The decision is currently unanswerable from outside: "why is this column a
// free-text box instead of a dropdown" has no answer short of reading the
// provider's source and re-running the count by hand.
type CardinalityProbe struct {
	Provider   string `json:"provider"`
	Connection string `json:"connection,omitempty"`
	Column     string `json:"column"`
	Field      string `json:"field,omitempty"`
	Distinct   int64  `json:"distinct"`
	Limit      int    `json:"limit"`
	Kind       string `json:"kind"` // the filter kind the count chose
	Cached     bool   `json:"cached"`
}

// InspectionRecord is one inspection-cache lookup this request made.
//
// Column filters, OpenSearch field mappings and SQL catalogs are all memoised
// behind inspect.Memo, and the cache already returns how the lookup went — it
// simply had no one to tell. Without it a first page and a warm page differ by
// seconds with nothing in the record to explain the gap, and "why did opening
// this profile take four seconds once" stays a mystery.
type InspectionRecord struct {
	// Policy is the cache class (inspect.CacheClass) and Key the entry within
	// it. The key is a digest of the request, so it correlates two lookups
	// without disclosing what was in them.
	Policy string `json:"policy"`
	Key    string `json:"key"`

	// ElapsedMS is what this caller waited: ~0 for a hit, the whole fill for a
	// miss. That difference is the reason this record exists.
	ElapsedMS float64 `json:"elapsedMs"`

	Cached bool   `json:"cached"`
	State  string `json:"state,omitempty"` // fresh | stale
	// Refreshing says a background fill is running behind the value served.
	Refreshing bool  `json:"refreshing,omitempty"`
	AgeMS      int64 `json:"ageMs"`

	// RefreshError is a failed refresh behind a value that was still served;
	// Error is a failure the caller actually got. Distinct because the first is
	// a warning about staleness and the second is why the page has no filters.
	RefreshError string `json:"refreshError,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ExecutionDetail is the expensive half, served for one record on demand.
type ExecutionDetail struct {
	Summary     ExecutionSummary       `json:"summary"`
	Operations  []*ProviderDiagnostics `json:"operations,omitempty"`
	HAR         *har.File              `json:"har,omitempty"`
	Logs        []LogLine              `json:"logs,omitempty"`
	Probes      []CardinalityProbe     `json:"probes,omitempty"`
	Inspections []InspectionRecord     `json:"inspections,omitempty"`

	// HARSensitive reports that credential capture was switched on, so the
	// entries above hold live secrets. Stated rather than refused: it is a
	// deliberate operator setting for replay, and a console that hands the file
	// to a colleague has to know which kind of file it is.
	HARSensitive bool `json:"harSensitive,omitempty"`
}

// DetailForLevel maps a capture level onto how much a run pays to be explained.
//
// Trace is the boundary because trace is where the connection policy first asks
// a backend for something it would not otherwise say — the SQL statement, the
// HTTP headers, ClickHouse's own log stream.
func DetailForLevel(level logger.LogLevel) DiagnosticDetail {
	if level >= logger.Trace {
		return DiagnosticFull
	}
	return DiagnosticRendered
}
