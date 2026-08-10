package query

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
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

func MarshalDiagnosticPreview(value any) []byte {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte(`{"error":"diagnostic preview could not be encoded"}`)
	}
	return encoded
}
