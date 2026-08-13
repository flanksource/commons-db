package query

import "time"

// ReconcileSideExecution is what one side of a reconciliation actually ran.
//
// It exists because a reconciliation's findings are only as trustworthy as the
// two reads behind them, and neither read is visible in the joined rows. "This
// key never arrived" is a claim about a query that ran once, against a
// connection, with filters, at a moment — and the rows alone record none of it.
type ReconcileSideExecution struct {
	Side     string `json:"side"`
	Profile  string `json:"profile"`
	Provider string `json:"provider"`

	// Query is the profile's query as authored, templates and all; the rendered
	// and native forms live on Diagnostics.Request.
	Query string `json:"query,omitempty"`

	// Filters are the resolved per-side filter values, sanitized the same way a
	// provider's options are.
	Filters map[string]any `json:"filters,omitempty"`

	Diagnostics *ProviderDiagnostics `json:"diagnostics,omitempty"`

	// Rows is what the join consumed from this side — after column transforms
	// and processors, and after a key range stopped the walk early.
	// Diagnostics.Response.ReturnedRows is what the provider handed over, so the
	// two differ whenever the profile drops or the range truncates.
	Rows  int `json:"rows"`
	Pages int `json:"pages,omitempty"`

	// DurationMS is wall clock spent reading this side. On a merged run the two
	// sides interleave and this includes the join's own time between pulls, so
	// BackendMS — the summed provider response time — is the number that
	// compares across modes.
	DurationMS float64 `json:"durationMs"`
	BackendMS  float64 `json:"backendMs,omitempty"`

	Truncated bool `json:"truncated,omitempty"`
}

// ReconcileProvenance is how a run happened: the mode it took and what each
// side asked. The config it ran under lives on the result, not here, so the two
// cannot disagree.
type ReconcileProvenance struct {
	Mode           ReconcileMode           `json:"mode,omitempty"`
	BufferedReason string                  `json:"buffered_reason,omitempty"`
	Source         *ReconcileSideExecution `json:"source,omitempty"`
	Dest           *ReconcileSideExecution `json:"dest,omitempty"`
	RanAt          time.Time               `json:"ran_at"`
}

// reconcileSideRecorder captures one side's execution. Both join modes drive it
// so they cannot disagree about what a side's provenance means — the buffered
// path hands its sink to Execute through the context, the merged path hands it
// to ExecutePages on the page request, and everything after that is identical.
type reconcileSideRecorder struct {
	side    string
	profile string
	filters map[string]any
	sink    *ProviderDiagnostics
	started time.Time
}

func newReconcileSideRecorder(side string, profile Profile, filters map[string]any) *reconcileSideRecorder {
	return &reconcileSideRecorder{
		side:    side,
		profile: profile.Name,
		filters: filters,
		// A walk recorder even on the buffered path: Execute drains pages too,
		// so a buffered side is a many-page read that happens to be collected
		// before the join starts.
		sink: NewWalkDiagnostics(profile.Provider.Type),
	}
}

func (r *reconcileSideRecorder) start() { r.started = time.Now() }

// finish closes the side's record. rows is what the join consumed, which only
// the caller can know — the sink counts what the provider returned.
func (r *reconcileSideRecorder) finish(profile Profile, rows int, truncated bool) *ReconcileSideExecution {
	diagnostics := r.sink.Snapshot()
	execution := &ReconcileSideExecution{
		Side:        r.side,
		Profile:     r.profile,
		Provider:    profile.Provider.Type,
		Query:       profile.Query,
		Filters:     SanitizeDiagnosticValues(r.filters),
		Diagnostics: diagnostics,
		Rows:        rows,
		DurationMS:  float64(time.Since(r.started)) / float64(time.Millisecond),
		Truncated:   truncated,
	}
	if diagnostics != nil {
		execution.Pages = diagnostics.Response.Pages
		execution.BackendMS = diagnostics.Response.DurationMS
	}
	return execution
}
