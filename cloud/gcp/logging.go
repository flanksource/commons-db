package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	loggingDefaultRoot = "https://logging.googleapis.com"
	loggingAPIPath     = "/v2"
)

// LoggingClient talks to the Cloud Logging REST API.
type LoggingClient struct{ *api }

// NewLoggingClient builds a Cloud Logging client.
func NewLoggingClient(opts Options) (*LoggingClient, error) {
	client, err := newAPI(opts, loggingDefaultRoot, loggingAPIPath)
	if err != nil {
		return nil, err
	}
	return &LoggingClient{api: client}, nil
}

// LogEntry is the subset of a Cloud Logging entry commons-db reads.
type LogEntry struct {
	LogName   string    `json:"logName"`
	InsertID  string    `json:"insertId"`
	Timestamp time.Time `json:"timestamp"`
	// Severity is the API enum name, e.g. "WARNING".
	Severity string `json:"severity"`
	Resource *struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Labels       map[string]string `json:"labels"`
	TextPayload  *string           `json:"textPayload"`
	JSONPayload  map[string]any    `json:"jsonPayload"`
	ProtoPayload map[string]any    `json:"protoPayload"`
}

// Payload returns the entry's payload: the text string, or the JSON/proto
// object. It is nil when the entry carries none.
func (e *LogEntry) Payload() any {
	switch {
	case e.JSONPayload != nil:
		return e.JSONPayload
	case e.ProtoPayload != nil:
		return e.ProtoPayload
	case e.TextPayload != nil:
		return *e.TextPayload
	}
	return nil
}

// SeverityName returns the severity in title case ("Warning"), or "" for the
// unset DEFAULT severity.
func (e *LogEntry) SeverityName() string {
	if e.Severity == "" || e.Severity == "DEFAULT" {
		return ""
	}
	return strings.ToUpper(e.Severity[:1]) + strings.ToLower(e.Severity[1:])
}

// ListEntriesOptions are the entries.list parameters commons-db uses.
type ListEntriesOptions struct {
	// ResourceNames are the parents to read, e.g. "projects/my-project".
	ResourceNames []string
	Filter        string
	// OrderBy is "timestamp asc" or "timestamp desc".
	OrderBy   string
	PageSize  int
	PageToken string
}

// EntriesPage is one page of log entries.
type EntriesPage struct {
	Entries       []*LogEntry `json:"entries"`
	NextPageToken string      `json:"nextPageToken"`
}

type listEntriesRequest struct {
	ResourceNames []string `json:"resourceNames"`
	Filter        string   `json:"filter,omitempty"`
	OrderBy       string   `json:"orderBy,omitempty"`
	PageSize      int      `json:"pageSize,omitempty"`
	PageToken     string   `json:"pageToken,omitempty"`
}

// ListEntries fetches one page of log entries.
func (l *LoggingClient) ListEntries(ctx context.Context, opts ListEntriesOptions) (*EntriesPage, error) {
	if len(opts.ResourceNames) == 0 {
		return nil, fmt.Errorf("gcp: listing log entries needs at least one resource name")
	}
	var page EntriesPage
	err := l.do(ctx, call{
		Method: http.MethodPost,
		URL:    l.base + "/entries:list",
		Body:   listEntriesRequest(opts),
		Out:    &page,
	})
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// EachEntry calls visit for every entry, following pages, until visit returns
// done=true, an error, or the entries run out.
func (l *LoggingClient) EachEntry(ctx context.Context, opts ListEntriesOptions, visit func(*LogEntry) (done bool, err error)) error {
	for {
		page, err := l.ListEntries(ctx, opts)
		if err != nil {
			return err
		}
		for _, entry := range page.Entries {
			if done, err := visit(entry); err != nil || done {
				return err
			}
		}
		if page.NextPageToken == "" {
			return nil
		}
		opts.PageToken = page.NextPageToken
	}
}
