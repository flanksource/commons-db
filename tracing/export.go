// What a host makes of a window of a capture's records beyond reading them:
// a download in one of its kind's formats, and a summary of them.

package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"

	"github.com/flanksource/commons-db/recordstore"
)

// ErrUnknownFormat refuses an export in a format its kind does not offer.
var ErrUnknownFormat = errors.New("unknown export format")

// ExportFormat is a format a kind's records download in: its name, the
// extension of the file it downloads as, and its media type.
type ExportFormat struct {
	Name string `json:"name"`
	Ext  string `json:"ext"`
	MIME string `json:"mime"`
}

// NDJSON is the format every kind exports in: its rows as stored, one JSON
// object per line.
var NDJSON = ExportFormat{Name: "ndjson", Ext: "ndjson", MIME: "application/x-ndjson"}

// Exporter is a TraceHandler that downloads its records in formats of its
// own, beyond NDJSON, such as HAR for HTTP exchanges.
type Exporter[R any] interface {
	Formats() []ExportFormat
	// Export writes rows, a window's records as Records reads them back, in
	// format, one of Formats.
	Export(format string, rows iter.Seq[R], w io.Writer) error
}

// Summarizer is a TraceHandler that summarizes a window of its records, as
// the variables an assertion over the window reads. The summary's count is
// always the window's records, whatever Summarize says.
type Summarizer[R any] interface {
	Summarize(rows iter.Seq[R]) (map[string]any, error)
}

// ExportFormats are the formats the kind exports in: NDJSON, then its own.
func (h *Handler[P, R]) ExportFormats() []ExportFormat {
	formats := []ExportFormat{NDJSON}
	if exporter, ok := h.handler.(Exporter[R]); ok {
		formats = append(formats, exporter.Formats()...)
	}
	return formats
}

func (h *Handler[P, R]) export(ctx context.Context, store scanner, ref TraceRef, format string, w io.Writer) error {
	if format == NDJSON.Name {
		return exportNDJSON(ctx, store, ref, w)
	}
	exporter, ok := h.handler.(Exporter[R])
	if !ok || !hasFormat(exporter.Formats(), format) {
		return fmt.Errorf("%w %q for trace kind %q", ErrUnknownFormat, format, ref.Kind)
	}
	rows, scan := h.records(store, ref.Stream).window(ctx, ref)
	return errors.Join(exporter.Export(format, rows, w), scan.err)
}

// summarize is the kind's summary of ref's window, its count set to the
// window's records. A kind that is no Summarizer is summarized by its count.
func (h *Handler[P, R]) summarize(ctx context.Context, store scanner, ref TraceRef) (map[string]any, error) {
	summary := map[string]any{}
	rows, scan := h.records(store, ref.Stream).window(ctx, ref)
	if summarizer, ok := h.handler.(Summarizer[R]); ok {
		var err error
		if summary, err = summarizer.Summarize(rows); err != nil {
			return nil, fmt.Errorf("summarize trace %s: %w", ref.Kind, err)
		}
		if summary == nil {
			return nil, fmt.Errorf("summarize trace %s: the kind summarized nothing", ref.Kind)
		}
	}
	if !scan.complete {
		for range rows {
		}
	}
	if scan.err != nil {
		return nil, scan.err
	}
	summary["count"] = int(scan.rows)
	return summary, nil
}

func (h *Handler[P, R]) records(store scanner, stream string) storedRecords[R] {
	return storedRecords[R]{store: store, stream: stream, jsonPaths: h.jsonPaths}
}

func (h *Handler[P, R]) recordType() reflect.Type { return reflect.TypeFor[R]() }

func (h *Handler[P, R]) jsonFields() []string { return h.jsonPaths }

// exportNDJSON writes ref's rows as stored, one JSON object per line.
func exportNDJSON(ctx context.Context, store scanner, ref TraceRef, w io.Writer) error {
	encoder := json.NewEncoder(w)
	return scanWindow(ctx, store, ref, func(_ int64, row recordstore.Row) error {
		return encoder.Encode(row)
	})
}

// validateFormats refuses formats a download could not be named by, and a
// name offered twice, NDJSON's among them.
func validateFormats(formats []ExportFormat) error {
	seen := map[string]bool{}
	for _, format := range formats {
		switch {
		case format.Name == "" || format.Ext == "" || format.MIME == "":
			return fmt.Errorf("export format %q needs a name, an extension and a media type", format.Name)
		case seen[format.Name]:
			return fmt.Errorf("export format %q twice", format.Name)
		}
		seen[format.Name] = true
	}
	return nil
}

func hasFormat(formats []ExportFormat, name string) bool {
	for _, format := range formats {
		if format.Name == name {
			return true
		}
	}
	return false
}
