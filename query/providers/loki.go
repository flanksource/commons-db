package providers

import (
	"fmt"
	"strconv"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/loki"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&lokiProvider{})
}

// lokiProvider runs a LogQL range query against Loki and returns one row per log
// line. HAR capture is maintained by the underlying loki searcher (feature
// "loki").
type lokiProvider struct{}

func (lokiProvider) Type() string { return "loki" }

type lokiOptions struct {
	// URL is an inline Loki base URL used when no stored connection is referenced.
	URL       string `json:"url,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
	Limit     string `json:"limit,omitempty"`
	Since     string `json:"since,omitempty"`
	Step      string `json:"step,omitempty"`
	Direction string `json:"direction,omitempty"`
}

func (lokiProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[lokiOptions](req.Options)
	if err != nil {
		return nil, err
	}

	request := loki.Request{
		Query:     req.Query,
		Since:     opts.Since,
		Step:      opts.Step,
		Direction: opts.Direction,
	}
	request.Start = opts.Start
	request.End = opts.End
	request.Limit = opts.Limit

	conn := connection.Loki{HTTPConnection: connection.HTTPConnection{ConnectionName: req.Connection}}
	if opts.URL != "" {
		conn.URL, err = resolveInlineURL(ctx, opts.URL, "loki")
		if err != nil {
			return nil, err
		}
	}

	searcher := loki.New(conn, nil)
	result, err := searcher.Search(ctx, request)
	if err != nil {
		return nil, err
	}

	return logResultToRows(result), nil
}

// Stream tails the query over Loki's websocket endpoint, which is the only one
// that serves lines as they are written — the range query Execute runs answers
// a window and stops.
//
// A tail is the future of a stream rather than a view of its past, so only the
// options that still mean something there carry over: the query, the instant
// the backfill starts at and how much of it to send. An end, a step or a
// direction describe a window nobody is asking for here.
func (lokiProvider) Stream(ctx context.Context, req query.ProviderRequest, emit func(query.Row)) error {
	opts, err := query.DecodeOptions[lokiOptions](req.Options)
	if err != nil {
		return err
	}

	request := loki.StreamRequest{Query: req.Query, Start: opts.Start}
	if opts.Limit != "" {
		// Loki's own default backfill takes over when the limit is absent, so a
		// limit nobody can read has to fail rather than quietly become that.
		if request.Limit, err = strconv.Atoi(opts.Limit); err != nil {
			return fmt.Errorf("limit %q is not a number: %w", opts.Limit, err)
		}
	}

	conn := connection.Loki{HTTPConnection: connection.HTTPConnection{ConnectionName: req.Connection}}
	if opts.URL != "" {
		conn.URL, err = resolveInlineURL(ctx, opts.URL, "loki")
		if err != nil {
			return err
		}
	}

	items, err := loki.New(conn, nil).Stream(ctx, request)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			// The websocket read is not bound to ctx — the dial's context covers
			// the handshake alone — so the tail can still be blocked on a frame
			// that will never arrive. Returning here rather than waiting for the
			// channel to close is what makes a stopped tail stop.
			return nil
		case item, open := <-items:
			if !open {
				return nil
			}
			if item.Error != nil {
				return item.Error
			}
			emit(logLineToRow(item.LogLine))
		}
	}
}

// logResultToRows flattens a logs.LogResult into generic rows. Shared by the
// log-backed providers.
func logResultToRows(result *logs.LogResult) []query.Row {
	if result == nil {
		return nil
	}

	rows := make([]query.Row, 0, len(result.Logs))
	for _, line := range result.Logs {
		if line == nil {
			continue
		}
		rows = append(rows, logLineToRow(line))
	}
	return rows
}

func logLineToRow(line *logs.LogLine) query.Row {
	row := query.Row{
		"timestamp": line.FirstObserved,
		"message":   line.Message,
	}
	// id identifies the line within its result, which is what an order ending in
	// it can page by. A backend that mints none simply contributes no key.
	if line.ID != "" {
		row["id"] = line.ID
	}
	// hash is the message with its variable parts tokenized out, which is what
	// makes "cel.dedupe" able to see two spellings of one error as one row. It
	// is already computed by the backends, and dropping it here would leave the
	// processor nothing but the raw message to key on.
	if line.Hash != "" {
		row["hash"] = line.Hash
	}
	if line.Count > 0 {
		row["count"] = line.Count
	}
	if line.LastObserved != nil {
		row["lastObserved"] = *line.LastObserved
	}
	if line.Severity != "" {
		row["severity"] = line.Severity
	}
	if line.Host != "" {
		row["host"] = line.Host
	}
	if line.Source != "" {
		row["source"] = line.Source
	}
	for k, v := range line.Labels {
		if _, taken := row[k]; !taken {
			row[k] = v
		}
	}
	return row
}

// logResultsToRows flattens the per-pod/container slice a Kubernetes search
// returns, promoting each result's Metadata onto the rows it produced. Metadata
// wins over a label of the same name: it states where the line actually came
// from, where a pod label named "namespace" is just a label.
func logResultsToRows(results []logs.LogResult) []query.Row {
	var rows []query.Row
	for i := range results {
		result := &results[i]
		for _, row := range logResultToRows(result) {
			for k, v := range result.Metadata {
				row[k] = v
			}
			rows = append(rows, row)
		}
	}
	return rows
}
