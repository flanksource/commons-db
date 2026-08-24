package query

// Recorder is one armed request's view of itself: the operations it issued, the
// HTTP traffic they rode on, the lines they logged, and the cardinality probes
// they paid for.
//
// It exists because the pieces are already recorded and then dropped. Every
// execution builds a ProviderDiagnostics (see prepareConnectionOperation) and
// every HTTP-family operation builds a har.Collector; both are read once, written
// to the log, and discarded. The recorder is what keeps them long enough for
// someone to look.
//
// Every method is nil-safe, in the same style as ProviderDiagnostics, so the
// unarmed path — which is every request that did not ask for this — costs one
// nil check per call site and no branches in the callers.

import (
	"sync"
	"time"

	"github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
)

const (
	// DefaultRecorderLogLines bounds the lines one request retains. A request
	// that logs more than this is one whose interesting lines are at the start.
	DefaultRecorderLogLines = 500

	// DefaultRecorderHAREntries mirrors observability.DefaultCollectorEntries,
	// which is what the per-operation collectors are already capped at.
	DefaultRecorderHAREntries = 100
)

type recorderKey struct{}

// WithRecorder returns a context whose executions record themselves into r.
//
// It does not install r as the context's HAR collector. A collector installed
// there would be the parent every operation forwards to, and the recorder reads
// each operation's own collector at Finish — so doing both would deliver every
// entry twice, and doing only the former would silently displace a CLI --har
// export that had already installed one.
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	ctx = ctx.WithValue(recorderKey{}, r)
	if r == nil {
		return ctx
	}
	// Inspection lookups happen deep inside the providers, several layers below
	// anything that knows which request it is serving, so the recorder is
	// installed as the cache's observer here rather than threaded down by hand.
	ctx = ctx.WithValue(inspection.ObserverKey{}, inspection.Observer(func(observation inspection.Observation) {
		r.RecordInspection(inspectionRecord(observation))
	}))
	// Same lift, same reason: a request that asked to rebuild what it inspects
	// says so once here rather than at every InspectionOptions on the way down.
	if r.RefreshInspection() {
		ctx = ctx.WithValue(inspection.RefreshKey{}, true)
	}
	return ctx
}

func inspectionRecord(observation inspection.Observation) InspectionRecord {
	record := InspectionRecord{
		Policy: observation.Policy, Key: observation.Key,
		ElapsedMS:    float64(observation.Elapsed) / float64(time.Millisecond),
		Cached:       observation.Cache.Cached,
		State:        string(observation.Cache.State),
		Refreshing:   observation.Cache.Refreshing,
		AgeMS:        observation.Cache.AgeMS,
		RefreshError: observation.Cache.LastRefreshError,
	}
	if observation.Err != nil {
		record.Error = observation.Err.Error()
	}
	return record
}

// RecorderFrom returns the recorder this request was armed with, or nil for an
// ordinary run.
func RecorderFrom(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(recorderKey{}).(*Recorder)
	return recorder
}

// RecorderOptions configures NewRecorder.
type RecorderOptions struct {
	ID     string
	Level  logger.LogLevel
	Source ExecutionSource

	// RefreshInspection makes every metadata lookup this request performs rebuild
	// rather than read what is cached — the console's "re-run inspection".
	//
	// Scoped to the request on purpose: it costs this caller the rebuild and
	// leaves everyone else's reads alone, unlike flushing the cache.
	RefreshInspection bool

	// MaxLogLines and MaxHAREntries bound what one record retains; zero takes
	// the defaults above.
	MaxLogLines   int
	MaxHAREntries int
}

// Recorder accumulates one request's record.
type Recorder struct {
	id                string
	level             logger.LogLevel
	source            ExecutionSource
	refreshInspection bool
	startedAt         time.Time
	maxLogLines       int
	maxHAREntries     int

	mu          sync.Mutex
	operations  []operationRecord
	logs        []LogLine
	logDropped  int
	probes      []CardinalityProbe
	inspections []InspectionRecord

	entries    []har.Entry
	harDropped int
	sensitive  bool
	logSeq     int64

	finished   bool
	duration   time.Duration
	rows       int
	status     int
	failure    error
	finishOnce sync.Once
}

type operationRecord struct {
	summary     OperationSummary
	diagnostics *ProviderDiagnostics
}

func NewRecorder(options RecorderOptions) *Recorder {
	if options.MaxLogLines <= 0 {
		options.MaxLogLines = DefaultRecorderLogLines
	}
	if options.MaxHAREntries <= 0 {
		options.MaxHAREntries = DefaultRecorderHAREntries
	}
	return &Recorder{
		id: options.ID, level: options.Level, source: options.Source,
		refreshInspection: options.RefreshInspection,
		startedAt:         time.Now(),
		maxLogLines:       options.MaxLogLines, maxHAREntries: options.MaxHAREntries,
	}
}

// ID is the correlation handle the response carries so a console can join its
// own request to this record.
func (r *Recorder) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// Level is the capture level this request was armed at.
func (r *Recorder) Level() logger.LogLevel {
	if r == nil {
		return logger.Info
	}
	return r.level
}

// RefreshInspection reports that this request asked to rebuild every metadata
// lookup it makes rather than read what is cached.
func (r *Recorder) RefreshInspection() bool {
	if r == nil {
		return false
	}
	return r.refreshInspection
}

// DiagnosticDetail is how much the executions under this recorder should pay to
// explain themselves. An unarmed run records what it was going to record anyway.
func (r *Recorder) DiagnosticDetail() DiagnosticDetail {
	if r == nil {
		return DiagnosticRendered
	}
	return DetailForLevel(r.level)
}

// Operation registers a provider operation that is about to start and returns
// the handle it reports back through. It returns nil for an unarmed run, which
// every method on the handle tolerates.
func (r *Recorder) Operation(provider string) *Operation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.operations = append(r.operations, operationRecord{
		summary: OperationSummary{Index: len(r.operations) + 1, Provider: provider},
	})
	return &Operation{recorder: r, index: len(r.operations)}
}

// Log appends one line. Sequence is assigned here so a console can order lines
// that arrived from two sources.
func (r *Recorder) Log(line LogLine) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.logs) >= r.maxLogLines {
		r.logDropped++
		return
	}
	r.logSeq++
	line.Sequence = r.logSeq
	line.RecordID = r.id
	if line.Time.IsZero() {
		line.Time = time.Now()
	}
	if line.Source == "" {
		line.Source = "request"
	}
	r.logs = append(r.logs, line)
}

// RecordProbe appends one cardinality probe.
func (r *Recorder) RecordProbe(probe CardinalityProbe) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probes = append(r.probes, probe)
}

// RecordInspection appends one inspection-cache lookup.
func (r *Recorder) RecordInspection(record InspectionRecord) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inspections = append(r.inspections, record)
}

// FinishOptions is what the transport knows about a finished request. Row
// counts are deliberately absent: the operations report their own, and a
// middleware that had to be told would be guessing for any request that ran
// more than one.
type FinishOptions struct {
	Status int
	Err    error
}

// Finish closes the record. It is idempotent: a request that both errors and
// then unwinds through a deferred close must not be recorded twice.
func (r *Recorder) Finish(options FinishOptions) {
	if r == nil {
		return
	}
	r.finishOnce.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.finished = true
		r.duration = time.Since(r.startedAt)
		r.status = options.Status
		r.failure = options.Err
	})
}

// Summary is the cheap half of the record, safe to push to every open console.
func (r *Recorder) Summary() ExecutionSummary {
	if r == nil {
		return ExecutionSummary{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summaryLocked()
}

func (r *Recorder) summaryLocked() ExecutionSummary {
	operations := make([]OperationSummary, 0, len(r.operations))
	rows := 0
	for _, operation := range r.operations {
		operations = append(operations, operation.summary)
		rows += operation.summary.Rows
	}
	summary := ExecutionSummary{
		ID: r.id, Source: r.source, StartedAt: r.startedAt,
		DurationMS: float64(r.duration) / float64(time.Millisecond),
		Rows:       rows, Status: r.status, Level: r.level.String(),
		Operations: operations,
		Counts: RecordCounts{
			Operations: len(r.operations), HAREntries: len(r.entries), HARDropped: r.harDropped,
			LogLines: len(r.logs), LogDropped: r.logDropped, Probes: len(r.probes),
			Inspections: len(r.inspections),
		},
	}
	if r.failure != nil {
		summary.Error = r.failure.Error()
	}
	return summary
}

// Detail is the expensive half, assembled for one caller that asked for it.
//
// Every slice is copied: the recorder outlives the response, and a caller
// ranging over the live slices while a late operation completes would race.
func (r *Recorder) Detail() ExecutionDetail {
	if r == nil {
		return ExecutionDetail{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	diagnostics := make([]*ProviderDiagnostics, 0, len(r.operations))
	for _, operation := range r.operations {
		if operation.diagnostics != nil {
			diagnostics = append(diagnostics, operation.diagnostics)
		}
	}
	detail := ExecutionDetail{
		Summary:      r.summaryLocked(),
		Operations:   diagnostics,
		Logs:         append([]LogLine(nil), r.logs...),
		Probes:       append([]CardinalityProbe(nil), r.probes...),
		Inspections:  append([]InspectionRecord(nil), r.inspections...),
		HARSensitive: r.sensitive,
	}
	if len(r.entries) > 0 {
		detail.HAR = &har.File{Log: har.Log{
			Version: "1.2",
			Creator: har.Creator{Name: har.CreatorName, Version: "0"},
			Pages:   []har.Page{},
			Entries: append([]har.Entry(nil), r.entries...),
		}}
	}
	return detail
}

// Operation is one provider operation's handle back to the recorder.
type Operation struct {
	recorder *Recorder
	index    int
}

// Index is the 1-based position of this operation within its request, which is
// what a log line carries to say which of several queries wrote it.
func (o *Operation) Index() int {
	if o == nil {
		return 0
	}
	return o.index
}

// Log files a line against this operation, so a console can say which of a
// request's several queries wrote it.
func (o *Operation) Log(line LogLine) {
	if o == nil {
		return
	}
	line.Operation = o.index
	o.recorder.Log(line)
}

// OperationResult is everything one finished operation contributes.
type OperationResult struct {
	Diagnostics *ProviderDiagnostics
	Entries     []har.Entry
	Dropped     int
	Sensitive   bool
	Duration    time.Duration
	Rows        int
	Err         error
}

// Complete files the operation's result. The caller guarantees it runs once —
// connectionOperation.Finish is sync.Once-guarded — so this does not re-guard.
func (o *Operation) Complete(result OperationResult) {
	if o == nil || o.recorder == nil {
		return
	}
	r := o.recorder
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.index < 1 || o.index > len(r.operations) {
		return
	}
	record := &r.operations[o.index-1]
	record.diagnostics = result.Diagnostics
	record.summary = operationSummary(o.index, result)

	r.sensitive = r.sensitive || result.Sensitive
	r.harDropped += result.Dropped
	for _, entry := range result.Entries {
		if len(r.entries) >= r.maxHAREntries {
			r.harDropped++
			continue
		}
		r.entries = append(r.entries, entry)
	}
}

func operationSummary(index int, result OperationResult) OperationSummary {
	summary := OperationSummary{
		Index:      index,
		DurationMS: float64(result.Duration) / float64(time.Millisecond),
		Rows:       result.Rows,
	}
	if result.Err != nil {
		summary.Error = result.Err.Error()
	}
	diagnostics := result.Diagnostics
	if diagnostics == nil {
		return summary
	}
	summary.Provider = diagnostics.Provider
	summary.Connection = diagnostics.Request.Connection
	summary.Query = logger.StripSecrets(diagnostics.Request.Query)
	summary.Method = diagnostics.Request.Method
	summary.URL = diagnostics.Request.URL
	summary.Status = diagnostics.Response.Status
	summary.Pages = diagnostics.Response.Pages
	if summary.Rows == 0 {
		summary.Rows = diagnostics.Response.ReturnedRows
	}
	if summary.Error == "" {
		summary.Error = diagnostics.Error
	}
	return summary
}
