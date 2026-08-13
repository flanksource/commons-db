package query

import (
	"encoding/json"
	"errors"
	"net/url"
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

type ProviderDiagnostics struct {
	Provider string                     `json:"provider"`
	Request  ProviderDiagnosticRequest  `json:"request"`
	Response ProviderDiagnosticResponse `json:"response,omitempty"`
	Error    string                     `json:"error,omitempty"`

	mu sync.Mutex
}

type ProviderDiagnosticRequest struct {
	Query     string         `json:"query,omitempty"`
	Arguments []any          `json:"arguments,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type ProviderDiagnosticResponse struct {
	DurationMS   float64        `json:"durationMs,omitempty"`
	ReturnedRows int            `json:"returnedRows,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
	Preview      string         `json:"preview,omitempty"`
	ContentType  string         `json:"contentType,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
}

func NewProviderDiagnostics(provider, query string, options map[string]any) *ProviderDiagnostics {
	return &ProviderDiagnostics{
		Provider: provider,
		Request: ProviderDiagnosticRequest{
			Query:   query,
			Options: sanitizeDiagnosticMap(options),
		},
	}
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
	d.Request.Options = sanitizeDiagnosticMap(options)
}

func (d *ProviderDiagnostics) RecordRequest(query string, arguments []any, details map[string]any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
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
	d.Response.DurationMS = float64(time.Since(started)) / float64(time.Millisecond)
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
	return &ProviderDiagnostics{
		Provider: d.Provider,
		Request: ProviderDiagnosticRequest{
			Query:     d.Request.Query,
			Arguments: cloneDiagnosticValues(d.Request.Arguments),
			Options:   sanitizeDiagnosticMap(d.Request.Options),
			Details:   sanitizeDiagnosticMap(d.Request.Details),
		},
		Response: ProviderDiagnosticResponse{
			DurationMS:   d.Response.DurationMS,
			ReturnedRows: d.Response.ReturnedRows,
			Details:      sanitizeDiagnosticMap(d.Response.Details),
			Preview:      d.Response.Preview,
			ContentType:  d.Response.ContentType,
			Truncated:    d.Response.Truncated,
		},
		Error: d.Error,
	}
}

// SanitizeDiagnosticValues blanks the values whose key names a credential, so a
// caller reporting its own map beside a provider's diagnostics holds both to the
// same rule rather than re-deriving it.
func SanitizeDiagnosticValues(values map[string]any) map[string]any {
	return sanitizeDiagnosticMap(values)
}

func sanitizeDiagnosticMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	clean := make(map[string]any, len(values))
	for key, value := range values {
		if diagnosticSecretKey(key) {
			clean[key] = "********"
			continue
		}
		if text, ok := value.(string); ok && diagnosticURLKey(key) {
			clean[key] = redactDiagnosticURL(text)
			continue
		}
		clean[key] = sanitizeDiagnosticValue(value)
	}
	return clean
}

func sanitizeDiagnosticValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeDiagnosticMap(typed)
	case []any:
		return cloneDiagnosticValues(typed)
	case []map[string]any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, sanitizeDiagnosticMap(item))
		}
		return items
	default:
		return value
	}
}

func cloneDiagnosticValues(values []any) []any {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, sanitizeDiagnosticValue(value))
	}
	return cloned
}

func diagnosticSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, token := range []string{
		"password", "passwd", "secret", "token", "authorization", "cookie",
		"api_key", "apikey", "client_secret", "access_key", "private_key",
	} {
		if normalized == token || strings.HasSuffix(normalized, "_"+token) {
			return true
		}
	}
	return false
}

// diagnosticURLKey names an option that holds a connection string. Its key says
// nothing about a credential, but its value routinely carries one.
func diagnosticURLKey(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
	case "url", "uri", "dsn", "address", "endpoint", "connection", "connection_string":
		return true
	default:
		return false
	}
}

// redactDiagnosticURL strips the credentials out of a connection string while
// leaving the part an operator reads it for — which host, which database.
//
// Both shapes a DSN comes in are handled, because both appear in provider
// options: a URL whose userinfo and query carry the secret, and a key=value
// string where a `password=` field does.
func redactDiagnosticURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return redactDiagnosticDSN(raw)
	}
	if parsed.User != nil {
		if name := parsed.User.Username(); name != "" {
			parsed.User = url.User(name)
		} else {
			parsed.User = nil
		}
	}
	values := parsed.Query()
	for key := range values {
		if diagnosticSecretKey(key) {
			values.Set(key, "********")
		}
	}
	if len(values) > 0 {
		parsed.RawQuery = values.Encode()
	}
	return parsed.String()
}

// redactDiagnosticDSN rewrites the secret-named fields of a key=value DSN and
// nothing else — separators included, so what is left reads as the original
// string with holes in it rather than as a reformatted one.
func redactDiagnosticDSN(raw string) string {
	var out strings.Builder
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i < len(raw) && raw[i] != ';' && raw[i] != ' ' {
			continue
		}
		segment := raw[start:i]
		if key, _, found := strings.Cut(segment, "="); found && diagnosticSecretKey(key) {
			out.WriteString(key + "=********")
		} else {
			out.WriteString(segment)
		}
		if i < len(raw) {
			out.WriteByte(raw[i])
		}
		start = i + 1
	}
	return out.String()
}

func MarshalDiagnosticPreview(value any) []byte {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte(`{"error":"diagnostic preview could not be encoded"}`)
	}
	return encoded
}
