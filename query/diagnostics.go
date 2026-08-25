package query

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/context"
)

const DiagnosticPreviewLimit = 256 << 10

type DiagnosticError struct {
	Err         error
	Diagnostics *ProviderDiagnostics
}

func (e *DiagnosticError) Error() string { return e.Err.Error() }
func (e *DiagnosticError) Unwrap() error { return e.Err }

func WithDiagnostics(err error, diagnostics *ProviderDiagnostics) error {
	if err == nil || diagnostics == nil {
		return err
	}
	diagnostics.RecordError(err)
	return &DiagnosticError{Err: err, Diagnostics: diagnostics.Snapshot()}
}

func DiagnosticsFromError(err error) *ProviderDiagnostics {
	var diagnostic *DiagnosticError
	if errors.As(err, &diagnostic) {
		return diagnostic.Diagnostics
	}
	return nil
}

type diagnosticSinkKey struct{}

// WithDiagnosticSink returns a context whose executions record the provider
// request they issue into d.
//
// ExecutePages takes its recorder from the PageRequest it is handed, which is
// the whole story for a paged read. Execute has none: a buffered profile's
// result comes from a walk the caller never describes, and a debug run that can
// explain the paged half of the profiles and not the buffered half explains the
// wrong half. The context is the only thing both paths already carry.
func WithDiagnosticSink(ctx context.Context, d *ProviderDiagnostics) context.Context {
	return ctx.WithValue(diagnosticSinkKey{}, d)
}

// DiagnosticSink returns the recorder WithDiagnosticSink attached, or nil when
// this is an ordinary run.
func DiagnosticSink(ctx context.Context) *ProviderDiagnostics {
	sink, _ := ctx.Value(diagnosticSinkKey{}).(*ProviderDiagnostics)
	return sink
}

// DiagnosticDetail says how much a run is willing to pay to be explained.
type DiagnosticDetail int

const (
	// DiagnosticRendered records what was sent and what came back, and nothing a
	// backend has to be asked for. It is what a run that is not a debug run can
	// afford — a reconciliation records itself on every execution, not only when
	// someone is watching.
	DiagnosticRendered DiagnosticDetail = iota

	// DiagnosticFull additionally records response previews and whatever
	// backend-side instrumentation the provider can switch on.
	DiagnosticFull
)

type ProviderDiagnostics struct {
	Provider string                     `json:"provider"`
	Request  ProviderDiagnosticRequest  `json:"request"`
	Response ProviderDiagnosticResponse `json:"response,omitempty"`
	Error    string                     `json:"error,omitempty"`

	mu sync.Mutex
	// walk records a many-page read rather than one request: the first statement
	// wins and the response fields accumulate. requested tracks whether that
	// first statement has been seen.
	walk      bool
	requested bool
	detail    DiagnosticDetail
	// armed says the provider has just described a request it is about to issue,
	// so the next HTTP exchange is the one carrying it. See HTTPTransport.
	armed bool
}

type ProviderDiagnosticRequest struct {
	// Query is the statement the provider actually issued — the first page's,
	// for a walk. Rendered is the profile's query once the engine templated it.
	// A provider that records nothing of its own leaves them equal.
	Query    string `json:"query,omitempty"`
	Rendered string `json:"rendered,omitempty"`
	// Connection is the reference or DSN the request was rendered against,
	// stripped of credentials.
	Connection string         `json:"connection,omitempty"`
	Arguments  []any          `json:"arguments,omitempty"`
	Options    map[string]any `json:"options,omitempty"`
	Details    map[string]any `json:"details,omitempty"`

	// Method, URL and Headers are the HTTP exchange the request rode on, for a
	// provider that speaks HTTP. The endpoint is stated nowhere the author can
	// read it — a connection names a host and the provider builds the path — so
	// it is the first thing anyone reproducing the call by hand has to guess.
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ProviderDiagnosticResponse struct {
	DurationMS   float64        `json:"durationMs,omitempty"`
	ReturnedRows int            `json:"returnedRows,omitempty"`
	Pages        int            `json:"pages,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	Preview      string         `json:"preview,omitempty"`
	ContentType  string         `json:"contentType,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`

	// Status and Headers are what the HTTP exchange answered, for a provider
	// that speaks HTTP.
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// DiagnosticOptions describes what a recorder records and how much it pays to.
//
// Walk records a many-page read as one request rather than as forty: the first
// statement issued is the one reported, and every page's rows and duration are
// summed into one response. A walk's last page is not what it ran — recording
// page forty the way a single request is recorded reports `OFFSET 19500` as the
// query, which is true of that page and false of the read.
//
// Detail and Walk are independent, which is the whole reason this is one
// constructor rather than two. Ordinary paged execution is a walk at
// DiagnosticRendered, because a profile read must not switch on per-page
// previews nobody asked for; an armed debug run of the same profile is the same
// walk at DiagnosticFull.
type DiagnosticOptions struct {
	Provider string
	Query    string
	Options  map[string]any
	Detail   DiagnosticDetail
	Walk     bool
}

func NewDiagnostics(options DiagnosticOptions) *ProviderDiagnostics {
	return &ProviderDiagnostics{
		Provider: options.Provider,
		walk:     options.Walk,
		detail:   options.Detail,
		Request: ProviderDiagnosticRequest{
			Query:   options.Query,
			Options: sanitizeDiagnosticMap(options.Options),
		},
	}
}

// WantsPreview reports whether this run pays for a response body preview and
// for backend-side instrumentation.
func (d *ProviderDiagnostics) WantsPreview() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.detail >= DiagnosticFull
}

// RecordConnection records the connection the request was rendered against. A
// connection:// reference is kept verbatim; an inline DSN is stripped of its
// credentials, because provenance that cannot be shared is provenance nobody
// reads.
func (d *ProviderDiagnostics) RecordConnection(connection string) {
	if d == nil || connection == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.HasPrefix(connection, "connection://") {
		d.Request.Connection = connection
		return
	}
	d.Request.Connection = redactDiagnosticURL(connection)
}

// RecordRendered seeds the diagnostics with the request the engine built — the
// templated query and the options it resolved to — so a provider that records
// nothing of its own still says what ran. A provider that does record replaces
// the query with the statement it actually issued.
func (d *ProviderDiagnostics) RecordRendered(query string, options map[string]any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Request.Query = query
	d.Request.Rendered = query
	d.Request.Options = sanitizeDiagnosticMap(options)
}

func (d *ProviderDiagnostics) RecordRequest(query string, arguments []any, details map[string]any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.walk && d.requested {
		return
	}
	d.requested = true
	// Whatever the provider sends next is what this statement travels in, which
	// is how HTTPTransport tells the search apart from the ping before it.
	d.armed = true
	d.Request.Query = query
	d.Request.Arguments = cloneDiagnosticValues(arguments)
	d.Request.Details = sanitizeDiagnosticMap(details)
}

func (d *ProviderDiagnostics) RecordResponse(started time.Time, rows int, details map[string]any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	elapsed := float64(time.Since(started)) / float64(time.Millisecond)
	if d.walk {
		d.Response.DurationMS += elapsed
		d.Response.ReturnedRows += rows
		d.Response.Pages++
		// The first page's details describe the walk — dialect, and the paging
		// mode the rest of the pages follow. Later pages differ only in offset,
		// so keeping the last would report the tail as the shape.
		if d.Response.Details == nil {
			d.Response.Details = sanitizeDiagnosticMap(details)
		}
		return
	}
	// Pages stays unset for a single request: a page count means something only
	// for a walk, and an omitted field says that better than a hardcoded 1.
	d.Response.DurationMS = elapsed
	d.Response.ReturnedRows = rows
	d.Response.Details = sanitizeDiagnosticMap(details)
}

func (d *ProviderDiagnostics) RecordError(err error) {
	if d == nil || err == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Error = err.Error()
}

func (d *ProviderDiagnostics) RecordPreview(contentType string, body []byte) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.detail < DiagnosticFull {
		return
	}
	d.Response.ContentType = contentType
	d.Response.Truncated = len(body) > DiagnosticPreviewLimit
	if d.Response.Truncated {
		body = body[:DiagnosticPreviewLimit]
	}
	d.Response.Preview = string(body)
}

func (d *ProviderDiagnostics) Snapshot() *ProviderDiagnostics {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Every field is copied by hand so the copy can be re-sanitized on the way
	// out. That makes an added field a silent omission rather than a compile
	// error — diagnostics_test.go asserts the whole round trip for that reason.
	return &ProviderDiagnostics{
		Provider: d.Provider,
		walk:     d.walk,
		detail:   d.detail,
		Request: ProviderDiagnosticRequest{
			Query:      d.Request.Query,
			Rendered:   d.Request.Rendered,
			Connection: d.Request.Connection,
			Arguments:  cloneDiagnosticValues(d.Request.Arguments),
			Options:    sanitizeDiagnosticMap(d.Request.Options),
			Details:    sanitizeDiagnosticMap(d.Request.Details),
			Method:     d.Request.Method,
			URL:        d.Request.URL,
			Headers:    cloneDiagnosticHeaders(d.Request.Headers),
		},
		Response: ProviderDiagnosticResponse{
			DurationMS:   d.Response.DurationMS,
			ReturnedRows: d.Response.ReturnedRows,
			Pages:        d.Response.Pages,
			Details:      sanitizeDiagnosticMap(d.Response.Details),
			Preview:      d.Response.Preview,
			ContentType:  d.Response.ContentType,
			Truncated:    d.Response.Truncated,
			Status:       d.Response.Status,
			Headers:      cloneDiagnosticHeaders(d.Response.Headers),
		},
		Error: d.Error,
	}
}
