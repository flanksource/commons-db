package tracequery

import (
	"context"
	"fmt"
	"slices"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

const maxSpans = 100000

func (s Service) Sources() ([]Source, error) {
	if s.Connections == nil {
		return nil, fmt.Errorf("trace source catalog is not configured")
	}
	result := []Source{{ID: "records", Name: "Recorded traces", Type: "records"}}
	connections, err := s.Connections.ListConnections()
	if err != nil {
		return nil, err
	}
	for _, c := range connections {
		if c.Type == models.ConnectionTypeOpenTelemetry || c.Type == models.ConnectionTypeOpenSearch {
			name := c.Name
			if c.Namespace != "" {
				name = c.Namespace + "/" + name
			}
			result = append(result, Source{ID: c.ID.String(), Name: name, Type: c.Type})
		}
	}
	return result, nil
}

func (s Service) Search(ctx context.Context, r Request) (SearchResult, error) {
	if err := r.validate(); err != nil {
		return SearchResult{}, err
	}
	var err error
	r, err = r.window(time.Now())
	if err != nil {
		return SearchResult{}, err
	}
	spans, err := s.load(ctx, r, nil)
	if err != nil {
		return SearchResult{}, err
	}
	ids := []string{}
	for _, span := range spans {
		if inWindow(span, r) && !slices.Contains(ids, span.TraceID) {
			ids = append(ids, span.TraceID)
		}
	}
	if len(ids) > 0 {
		full := r
		full.From, full.Until = "", ""
		spans, err = s.load(ctx, full, ids)
		if err != nil {
			return SearchResult{}, err
		}
	}
	return Search(spans, r)
}

func (s Service) Trace(ctx context.Context, r Request, id string) (Detail, error) {
	if id == "" {
		return Detail{}, fmt.Errorf("trace ID is required")
	}
	if err := r.validate(); err != nil {
		return Detail{}, err
	}
	r.TraceID = id
	r.From, r.Until = "", ""
	spans, err := s.load(ctx, r, []string{id})
	if err != nil {
		return Detail{}, err
	}
	return Trace(spans, id)
}

func (s Service) load(ctx context.Context, r Request, ids []string) ([]Span, error) {
	if s.Read != nil {
		return s.Read(ctx, r, ids)
	}
	if r.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	if s.Connections == nil {
		return nil, fmt.Errorf("trace source catalog is not configured")
	}
	c, err := s.Connections.ResolveConnection(dbcontext.NewContext(ctx), r.Source)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("source connection %q not found", r.Source)
	}
	return ReadOpenSearch(s.Connections.Attach(dbcontext.NewContext(ctx)), r, c, ids)
}

// ReadSpans reads the spans selected by a host without assembling a search.
func (s Service) ReadSpans(ctx context.Context, r Request, ids []string) ([]Span, error) {
	return s.load(ctx, r, ids)
}

func (r Request) window(now time.Time) (Request, error) {
	until := now.UTC()
	var err error
	if r.Until != "" {
		until, err = time.Parse(time.RFC3339Nano, r.Until)
		if err != nil {
			return r, fmt.Errorf("until: %w", err)
		}
	}
	lookback := r.Lookback
	if lookback == "" {
		lookback = "1h"
	}
	d, err := time.ParseDuration(lookback)
	if err != nil || d <= 0 {
		return r, fmt.Errorf("lookback must be a positive duration")
	}
	from := until.Add(-d)
	if r.From != "" {
		from, err = time.Parse(time.RFC3339Nano, r.From)
		if err != nil {
			return r, fmt.Errorf("from: %w", err)
		}
	}
	if !from.Before(until) {
		return r, fmt.Errorf("from must precede until")
	}
	r.From, r.Until = from.UTC().Format(time.RFC3339Nano), until.UTC().Format(time.RFC3339Nano)
	return r, nil
}
