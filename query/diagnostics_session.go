package query

import (
	"maps"
	"slices"
	"time"

	"github.com/flanksource/commons-db/query/sqlactivity"
)

// DiagnosticSampleLimit is how many server activity samples a recorder keeps;
// older ones are counted as samplesDropped.
const DiagnosticSampleLimit = 100

// ServerSession is the server session a statement ran on, read on the pinned
// connection before the statement: the SQL Server SPID, Postgres backend pid or
// MySQL connection id as SessionID, or ClickHouse's query id as QueryID. Page
// is the 1-based page of a paged read it ran for, zero for a program.
type ServerSession struct {
	Engine    string `json:"engine"`
	SessionID int64  `json:"sessionId,omitempty"`
	QueryID   string `json:"queryId,omitempty"`
	Server    string `json:"server,omitempty"`
	Database  string `json:"database,omitempty"`
	User      string `json:"user,omitempty"`
	Page      int    `json:"page,omitempty"`
}

// WantsSession reports whether this run pays a round trip to identify the
// server session a statement runs on.
func (d *ProviderDiagnostics) WantsSession() bool {
	if d == nil {
		return false
	}
	return d.onSession != nil || d.onSample != nil || d.autoDiagnose != 0
}

// AutoDiagnoseAfter is how long a statement runs before its session is
// sampled: zero never samples, a negative value samples from the start.
func (d *ProviderDiagnostics) AutoDiagnoseAfter() time.Duration {
	if d == nil {
		return 0
	}
	return d.autoDiagnose
}

// RecordSession records the session the next statement runs on, as
// Request.Details["session"], and hands it to OnSession.
func (d *ProviderDiagnostics) RecordSession(session ServerSession) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.session = &session
	d.applySessionDetails()
	onSession := d.onSession
	d.mu.Unlock()
	if onSession != nil {
		onSession(session)
	}
}

// RecordSample hands a server activity sample of the running statement to
// OnSample and keeps the last DiagnosticSampleLimit in
// Response.Details["samples"].
func (d *ProviderDiagnostics) RecordSample(sample sqlactivity.Sample) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.samples = append(d.samples, sample)
	if over := len(d.samples) - DiagnosticSampleLimit; over > 0 {
		d.samples = slices.Clone(d.samples[over:])
		d.samplesDropped += over
	}
	d.applySessionDetails()
	onSample := d.onSample
	d.mu.Unlock()
	if onSample != nil {
		onSample(sample)
	}
}

// RecordSampleError records why sampling the session stopped, as
// Response.Details["sampleError"], and hands it to OnSampleError. It fails the
// diagnostics, not the statement.
func (d *ProviderDiagnostics) RecordSampleError(err error) {
	if d == nil || err == nil {
		return
	}
	d.mu.Lock()
	d.sampleError = err.Error()
	d.applySessionDetails()
	onSampleError := d.onSampleError
	d.mu.Unlock()
	if onSampleError != nil {
		onSampleError(err)
	}
}

// applySessionDetails writes the session and samples into the details, which
// RecordRequest and RecordResponse replace wholesale. Each write is a fresh
// map, since a request handed to OnRequest shares the one it was given.
func (d *ProviderDiagnostics) applySessionDetails() {
	if d.session != nil {
		d.Request.Details = withDiagnosticDetail(d.Request.Details, "session", *d.session)
	}
	if len(d.samples) > 0 {
		d.Response.Details = withDiagnosticDetail(d.Response.Details, "samples", slices.Clone(d.samples))
	}
	if d.samplesDropped > 0 {
		d.Response.Details = withDiagnosticDetail(d.Response.Details, "samplesDropped", d.samplesDropped)
	}
	if d.sampleError != "" {
		d.Response.Details = withDiagnosticDetail(d.Response.Details, "sampleError", d.sampleError)
	}
}

func withDiagnosticDetail(details map[string]any, key string, value any) map[string]any {
	updated := maps.Clone(details)
	if updated == nil {
		updated = map[string]any{}
	}
	updated[key] = value
	return updated
}
