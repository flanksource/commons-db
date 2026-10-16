// A window of exchanges as a HAR 1.2 file, and as the summary a fixture
// asserts on.

package http

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"mime"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/flanksource/commons/har"

	"github.com/flanksource/commons-db/tracing"
)

// HAR is the format an http window downloads in besides NDJSON.
var HAR = tracing.ExportFormat{Name: "har", Ext: "har", MIME: "application/json"}

// maxSummaryRequests caps the requests a summary lists; its counts are always
// over every exchange.
const maxSummaryRequests = 200

func (traffic) Formats() []tracing.ExportFormat { return []tracing.ExportFormat{HAR} }

func (traffic) Export(format string, rows iter.Seq[Exchange], w io.Writer) error {
	return Export(format, rows, w)
}

func (traffic) Summarize(rows iter.Seq[Exchange]) (map[string]any, error) { return Summarize(rows) }

// Export writes rows in format, which must be HAR: a HAR 1.2 file, streamed
// one entry at a time. Every kind whose records are exchanges exports so.
func Export(format string, rows iter.Seq[Exchange], w io.Writer) error {
	if format != HAR.Name {
		return fmt.Errorf("%w %q", tracing.ErrUnknownFormat, format)
	}
	creator, err := json.Marshal(har.Creator{Name: har.CreatorName, Version: "0"})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, `{"log":{"version":"1.2","creator":%s,"pages":[],"entries":[`, creator); err != nil {
		return err
	}
	separator := ""
	for exchange := range rows {
		entry, err := json.Marshal(ToEntry(exchange))
		if err != nil {
			return fmt.Errorf("encode HAR entry %s %s: %w", exchange.Method, exchange.URL, err)
		}
		if _, err := fmt.Fprintf(w, "%s\n%s", separator, entry); err != nil {
			return err
		}
		separator = ","
	}
	_, err = io.WriteString(w, "\n]}}\n")
	return err
}

// Entry is a HAR entry with what an exchange observed beyond the fields of
// har.Entry: HAR 1.2's serverIPAddress and comment, and as custom fields,
// underscore-prefixed as the spec reserves them, a tunnel's byte counts and
// the encoding of a response body that is not text.
type Entry struct {
	har.Entry
	ServerIPAddress string `json:"serverIPAddress,omitempty"`
	Comment         string `json:"comment,omitempty"`
	Tunnelled       bool   `json:"_tunnelled,omitempty"`
	BytesIn         int64  `json:"_bytesIn,omitempty"`
	BytesOut        int64  `json:"_bytesOut,omitempty"`
	Encoding        string `json:"_encoding,omitempty"`
}

// ToEntry is exchange as the HAR entry it was captured from.
func ToEntry(exchange Exchange) Entry {
	return Entry{
		Entry: har.Entry{
			StartedDateTime: exchange.StartedAt.Format(time.RFC3339Nano), Time: exchange.DurationMs,
			Request: exchange.Request, Response: exchange.Response, Timings: exchange.Timings, Error: exchange.Error,
		},
		ServerIPAddress: exchange.ServerIPAddress, Comment: exchange.Comment,
		Tunnelled: exchange.Tunnelled, BytesIn: exchange.BytesIn, BytesOut: exchange.BytesOut, Encoding: exchange.Encoding,
	}
}

// Summarize counts the exchanges by method, status and host, and lists the
// first maxSummaryRequests of them. An exchange that failed, with a 4xx or
// 5xx or with no response at all, is an error. Status keys are strings, so a
// CEL assertion reads statuses["200"] as it reads methods["GET"].
func Summarize(rows iter.Seq[Exchange]) (map[string]any, error) {
	methods, statuses, hosts := map[string]int{}, map[string]int{}, map[string]bool{}
	requests := []map[string]any{}
	entries, errors := 0, 0
	for exchange := range rows {
		entries++
		host, path, err := splitURL(exchange.URL)
		if err != nil {
			return nil, err
		}
		hosts[host] = true
		methods[exchange.Method]++
		statuses[strconv.Itoa(exchange.Status)]++
		if exchange.Error != "" || exchange.Status >= 400 {
			errors++
		}
		if len(requests) < maxSummaryRequests {
			requests = append(requests, requestVars(exchange, host, path))
		}
	}
	return map[string]any{
		"entries": entries, "errors": errors, "hosts": slices.Sorted(maps.Keys(hosts)),
		"methods": methods, "statuses": statuses, "requests": requests,
	}, nil
}

// requestVars is one exchange as a summary lists it: its request and response
// lines, headers and query as name → first value, and its bodies. A response
// body that is not text (an Encoding) is left empty, and response_json is set
// only for a whole application/json body, never a truncated one.
func requestVars(exchange Exchange, host, path string) map[string]any {
	content := exchange.Response.Content
	vars := map[string]any{
		"method": exchange.Method, "url": exchange.URL, "host": host, "path": path,
		"status": exchange.Status, "duration_ms": exchange.DurationMs, "error": exchange.Error,
		"mime": content.MimeType, "tunnelled": exchange.Tunnelled,
		"request_headers":  firstValues(exchange.Request.Headers, func(h har.Header) (string, string) { return h.Name, h.Value }),
		"response_headers": firstValues(exchange.Response.Headers, func(h har.Header) (string, string) { return h.Name, h.Value }),
		"query":            firstValues(exchange.Request.QueryString, func(q har.QueryString) (string, string) { return q.Name, q.Value }),
		"body":             "", "response_size": content.Size, "response_body": "",
	}
	if exchange.Request.PostData != nil {
		vars["body"] = exchange.Request.PostData.Text
	}
	if exchange.Encoding == "" {
		vars["response_body"] = content.Text
	}
	if mediaType, _, _ := mime.ParseMediaType(content.MimeType); mediaType == "application/json" &&
		content.Text != "" && exchange.Encoding == "" && !content.Truncated {
		var parsed any
		if json.Unmarshal([]byte(content.Text), &parsed) == nil {
			vars["response_json"] = parsed
		}
	}
	return vars
}

// firstValues flattens HAR's ordered pairs into a lookup; a repeated name
// keeps its first value.
func firstValues[T any](pairs []T, pair func(T) (string, string)) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if name, value := pair(p); out[name] == "" {
			out[name] = value
		}
	}
	return out
}

// splitURL is the host, without its port, and the path of raw.
func splitURL(raw string) (host, path string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("summarize exchange: %w", err)
	}
	return parsed.Hostname(), parsed.Path, nil
}
