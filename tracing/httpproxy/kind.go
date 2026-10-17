// Package httpproxy is the http_proxy trace kind: a recording HTTP proxy the
// processes a capture observes are pointed at through the environment it
// exports, storing each exchange it relays as an http.Exchange.
package httpproxy

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	tracehttp "github.com/flanksource/commons-db/tracing/http"
)

// Kind is the name the http_proxy kind registers under.
const Kind = "http_proxy"

// maxValueBytes caps any one value an exchange stores: a body at maxBodies,
// base64-encoded when it is not text, fits under it.
const maxValueBytes = 6 << 20

type proxy struct{}

func (proxy) Params() Params { return Params{Mode: ModeConnect, Bodies: defaultBodies} }

func (proxy) Schema() recordresults.ResultType[tracehttp.Exchange] {
	return recordresults.ResultType[tracehttp.Exchange]{Title: "HTTP proxy traffic", TimeColumn: "startedAt"}
}

// Prepare starts the proxy, and under mitm its CA, before the session runs,
// exporting the environment that points a process at them. Each exchange is
// emitted without waiting: it arrives on the goroutine relaying the request,
// which a full buffer must never stall.
func (proxy) Prepare(ctx dbcontext.Context, params Params, records tracing.Emitter[tracehttp.Exchange]) (dbcontext.Context, func() error, error) {
	bodies, err := params.bodyLimit()
	if err != nil {
		return ctx, nil, err
	}
	ca, caDir, caPath, err := mitmAuthority(params.Mode)
	if err != nil {
		return ctx, nil, err
	}
	emit := func(exchange tracehttp.Exchange) { records.TryEmit(exchange) }
	rec, err := listen(params, newPolicy(bodies, params.Redact), ca, emit, records.Warn)
	if err != nil {
		return ctx, nil, errors.Join(err, removeDir(caDir))
	}
	for _, variable := range proxyEnv(rec.addr(), params.NoProxy, caPath) {
		records.Export(variable[0], variable[1])
	}
	metadata := []query.SessionMetadata{{Name: "mode", Label: "Mode", Value: string(params.Mode)}}
	if caPath != "" {
		metadata = append(metadata, query.SessionMetadata{Name: "ca", Label: "CA certificate", Value: caPath})
	}
	records.Describe(rec.addr(), metadata...)
	return ctx, func() error { return errors.Join(rec.close(), removeDir(caDir)) }, nil
}

// mitmAuthority generates a mitm capture's CA and writes its certificate; a
// connect capture has none.
func mitmAuthority(mode Mode) (ca *authority, dir, path string, err error) {
	if mode != ModeMITM {
		return nil, "", "", nil
	}
	if ca, err = newAuthority(); err != nil {
		return nil, "", "", err
	}
	dir, path, err = ca.writeCert()
	return ca, dir, path, err
}

func removeDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove ca directory: %w", err)
	}
	return nil
}

// proxyEnv routes a process through the proxy at addr. Both cases are set,
// curl reading the lowercase names and Node the uppercase. NO_PROXY bypasses
// the proxy itself and noProxy, but not loopback at large: a server the host
// started on 127.0.0.1 is the traffic most worth recording. Under mitm the
// process's TLS stack is pointed at caPath.
func proxyEnv(addr string, noProxy []string, caPath string) [][2]string {
	proxyURL := "http://" + addr
	bypass := strings.Join(append([]string{addr}, noProxy...), ",")
	env := [][2]string{
		{"HTTP_PROXY", proxyURL}, {"http_proxy", proxyURL},
		{"HTTPS_PROXY", proxyURL}, {"https_proxy", proxyURL},
		{"NO_PROXY", bypass}, {"no_proxy", bypass},
	}
	if caPath != "" {
		env = append(env, trustEnv(caPath)...)
	}
	return env
}

// Handle waits for the capture to stop: the proxy Prepare started emits
// every exchange.
func (proxy) Handle(ctx dbcontext.Context, _ Params, _ tracing.Emitter[tracehttp.Exchange], _ tracing.Records[tracehttp.Exchange]) error {
	<-ctx.Done()
	return nil
}

func (proxy) Formats() []tracing.ExportFormat { return []tracing.ExportFormat{tracehttp.HAR} }

func (proxy) Export(format string, rows iter.Seq[tracehttp.Exchange], w io.Writer) error {
	return tracehttp.Export(format, rows, w)
}

// Summarize is the http kind's summary, with the tunnels among the exchanges
// and the bytes that crossed them, in from servers and out from clients.
func (proxy) Summarize(rows iter.Seq[tracehttp.Exchange]) (map[string]any, error) {
	var tunnelled int
	var bytesIn, bytesOut int64
	summary, err := tracehttp.Summarize(func(yield func(tracehttp.Exchange) bool) {
		for exchange := range rows {
			if exchange.Tunnelled {
				tunnelled++
			}
			bytesIn, bytesOut = bytesIn+exchange.BytesIn, bytesOut+exchange.BytesOut
			if !yield(exchange) {
				return
			}
		}
	})
	if err != nil {
		return nil, err
	}
	summary["tunnelled"], summary["bytes_in"], summary["bytes_out"] = tunnelled, bytesIn, bytesOut
	return summary, nil
}

// NewKind is the http_proxy trace kind: exchanges masked, capped at 6MiB a
// value, and their bodies stored as structure when they hold JSON.
func NewKind() *tracing.Handler[Params, tracehttp.Exchange] {
	return tracing.NewHandler[Params, tracehttp.Exchange](proxy{}, tracing.Capabilities{Live: true}).
		WithSecretMasking().
		WithTruncation(maxValueBytes).
		WithJSONProcessor(tracehttp.RequestBody, tracehttp.ResponseBody)
}
