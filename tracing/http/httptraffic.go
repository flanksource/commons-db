// Package http is the http trace kind: it records the outbound HTTP
// exchanges commons-db makes for chosen features, one HAR entry per record.
package http

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons/har"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// maxValueBytes caps any one value an exchange stores.
const maxValueBytes = 1 << 20

// RequestBody and ResponseBody are where an exchange holds its bodies, as
// paths WithJSONProcessor takes.
const (
	RequestBody  = "request.postData.text"
	ResponseBody = "response.content.text"
)

// Params choose the features whose exchanges a capture records.
type Params struct {
	// Features name the transports to observe, as their HAR levels do:
	// http, prometheus, loki, opensearch, git, ...
	Features []string `json:"features" clicky:"required,title=Features"`
	// DedupWindow, a duration such as 1h, stores an exchange only once within
	// it: the same method, URL, status and bodies. Empty stores every one.
	DedupWindow string `json:"dedupWindow,omitempty" clicky:"title=Dedup window"`
}

func (p Params) Validate() error {
	if len(p.Features) == 0 {
		return errors.New("name at least one feature to observe")
	}
	for _, feature := range p.Features {
		if strings.TrimSpace(feature) == "" {
			return errors.New("a feature name must not be blank")
		}
	}
	if _, err := p.window(); err != nil {
		return err
	}
	return nil
}

func (p Params) window() (time.Duration, error) {
	if p.DedupWindow == "" {
		return 0, nil
	}
	window, err := time.ParseDuration(p.DedupWindow)
	if err != nil || window < 0 {
		return 0, fmt.Errorf("dedupWindow %q is not a duration such as 1h", p.DedupWindow)
	}
	return window, nil
}

// Exchange is one outbound request and its response.
type Exchange struct {
	tracing.Truncation
	StartedAt  time.Time    `json:"startedAt" pretty:"label=Started" sort:"startedAt"`
	Feature    string       `json:"feature" filter:"terms"`
	Method     string       `json:"method" filter:"terms"`
	Host       string       `json:"host" filter:"terms"`
	URL        string       `json:"url" pretty:"label=URL" filter:"text"`
	Status     int          `json:"status" filter:"terms"`
	DurationMs float64      `json:"durationMs" pretty:"label=Duration,type=duration,unit=ms" sort:"durationMs"`
	Request    har.Request  `json:"request" pretty:"hide"`
	Response   har.Response `json:"response" pretty:"hide"`
	// Error is why the exchange got no response, such as a refused connection.
	Error string `json:"error,omitempty" filter:"text"`
	// Timings break DurationMs down where the capture measured the phases; a
	// phase it did not measure is -1.
	Timings         har.Timings `json:"timings" pretty:"hide"`
	ServerIPAddress string      `json:"serverIPAddress,omitempty" pretty:"label=Server IP"`
	// Tunnelled marks a CONNECT tunnel a proxy relayed without decrypting it:
	// there is no method, path, status or body inside it to record, only the
	// bytes that crossed it, in from the server and out from the client.
	Tunnelled bool  `json:"tunnelled,omitempty"`
	BytesIn   int64 `json:"bytesIn,omitempty" pretty:"label=Bytes in"`
	BytesOut  int64 `json:"bytesOut,omitempty" pretty:"label=Bytes out"`
	// Encoding is the response content's encoding: base64 for a body that is
	// not UTF-8 text.
	Encoding string `json:"encoding,omitempty" pretty:"hide"`
	// Comment says what the capture left out of the exchange, such as a body
	// it cut or an event stream still open when it ended.
	Comment string `json:"comment,omitempty" filter:"text"`
}

// FromEntry is the exchange feature's transport captured as entry.
func FromEntry(feature string, entry har.Entry) Exchange {
	startedAt, _ := time.Parse(time.RFC3339, entry.StartedDateTime)
	exchange := Exchange{
		StartedAt: startedAt, Feature: feature, Method: entry.Request.Method, URL: entry.Request.URL,
		Status: entry.Response.Status, DurationMs: entry.Time, Request: entry.Request, Response: entry.Response,
		Error: entry.Error, Timings: entry.Timings,
	}
	if parsed, err := url.Parse(entry.Request.URL); err == nil {
		exchange.Host = parsed.Host
	}
	return exchange
}

// Key identifies an exchange for deduplication: its method, URL, status, and a
// digest of both bodies.
func Key(exchange Exchange) string {
	digest := sha256.New()
	if exchange.Request.PostData != nil {
		digest.Write([]byte(exchange.Request.PostData.Text))
	}
	digest.Write([]byte{0})
	digest.Write([]byte(exchange.Response.Content.Text))
	return exchange.Method + " " + exchange.URL + " " + strconv.Itoa(exchange.Status) + " " + hex.EncodeToString(digest.Sum(nil))
}

type traffic struct{}

func (traffic) Params() Params { return Params{} }

func (traffic) Schema() recordresults.ResultType[Exchange] {
	return recordresults.ResultType[Exchange]{Title: "HTTP traffic", TimeColumn: "startedAt"}
}

// Prepare observes every feature before the session runs, so no exchange
// made once it runs is missed. Each exchange is emitted without waiting: it
// arrives on the goroutine of the request it records, which a full buffer
// must never stall.
func (traffic) Prepare(ctx dbcontext.Context, params Params, records tracing.Emitter[Exchange]) (dbcontext.Context, func() error, error) {
	var releases []func()
	for _, feature := range params.Features {
		config := ctx.HARConfig(feature)
		config.MaxEntries = observability.DefaultCollectorEntries
		// A trace store keeps what it captures: credentials stay masked
		// whatever the process's HAR capture is told.
		config.CaptureSensitive = false
		collector := har.NewCollectorWithHandler(config, func(entry *har.Entry) {
			records.TryEmit(FromEntry(feature, *entry))
		})
		releases = append(releases, connection.ObserveHTTP(feature, collector))
	}
	return ctx, func() error {
		for _, release := range releases {
			release()
		}
		return nil
	}, nil
}

// Handle waits for the capture to stop: the observers Prepare installed emit
// every exchange.
func (traffic) Handle(ctx dbcontext.Context, _ Params, _ tracing.Emitter[Exchange], _ tracing.Records[Exchange]) error {
	<-ctx.Done()
	return nil
}

// Kind is the http trace kind: exchanges masked, capped at 1MiB a value, their
// request and response bodies stored as structure when they hold JSON, and
// deduplicated within the params' window.
func Kind() *tracing.Handler[Params, Exchange] {
	return tracing.NewHandler[Params, Exchange](traffic{}, tracing.Capabilities{Live: true}).
		WithDeduplication(Key, func(p Params) time.Duration {
			window, _ := p.window()
			return window
		}).
		WithSecretMasking().
		WithTruncation(maxValueBytes).
		WithJSONProcessor(RequestBody, ResponseBody)
}
