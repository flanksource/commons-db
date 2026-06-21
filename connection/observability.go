package connection

import (
	netHTTP "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons/har"
	commonsHTTP "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/logger/httpretty"
	"github.com/flanksource/commons/properties"
)

// observabilityContext is implemented by context types that drive per-feature
// HTTP/HAR observability (commons-db/context.Context implements it). The HAR
// helpers below take `any` and degrade gracefully when the context does not
// implement it, so they remain usable with only an explicit collector.
type observabilityContext interface {
	HARCollector() *har.Collector
	EffectiveHARCollector(feature string, explicit *har.Collector) *har.Collector
	EffectiveHARLevel(feature string) (logger.LogLevel, string)
	HTTPLoggingContent(feature string) (bool, bool)
}

func effectiveHARCollector(ctx any, feature string, explicit *har.Collector) *har.Collector {
	if c, ok := ctx.(observabilityContext); ok {
		return c.EffectiveHARCollector(feature, explicit)
	}
	return explicit
}

// ApplyHTTPObservability wraps a RoundTripper with HAR capture and HTTP logging
// for the given feature, honouring the context's effective levels.
func ApplyHTTPObservability(ctx any, feature string, base netHTTP.RoundTripper, explicit *har.Collector) netHTTP.RoundTripper {
	if base == nil {
		base = netHTTP.DefaultTransport
	}
	if middleware := harCollectorMiddleware(ctx, feature, explicit); middleware != nil {
		base = middleware(base)
	}
	if c, ok := ctx.(observabilityContext); ok {
		headers, bodies := c.HTTPLoggingContent(feature)
		base = httpLoggerWithContent(base, headers, bodies)
	}
	return base
}

// ApplyHTTPClientObservability attaches HAR capture and HTTP logging to a
// commons HTTP client for the given feature. It returns a middleware suitable
// for reuse on derived (e.g. token) clients, or nil when nothing was attached.
func ApplyHTTPClientObservability(ctx any, feature string, client *commonsHTTP.Client, explicit *har.Collector) middlewares.Middleware {
	if client == nil {
		return nil
	}

	var tokenTransport middlewares.Middleware
	level := logger.Info
	if c, ok := ctx.(observabilityContext); ok {
		level, _ = c.EffectiveHARLevel(feature)
	}
	if explicit != nil && level < logger.Debug {
		level = logger.Trace
	}

	if collector := effectiveHARCollector(ctx, feature, explicit); collector != nil && level >= logger.Debug {
		if level >= logger.Trace {
			client.HARCollector(collector)
		} else {
			middleware := metadataHARMiddleware(collector)
			client.Use(middleware)
			tokenTransport = middleware
		}
	}

	if c, ok := ctx.(observabilityContext); ok {
		headers, bodies := c.HTTPLoggingContent(feature)
		if headers {
			logMiddleware := func(rt netHTTP.RoundTripper) netHTTP.RoundTripper {
				return httpLoggerWithContent(rt, headers, bodies)
			}
			client.Use(logMiddleware)
			if tokenTransport == nil {
				tokenTransport = logMiddleware
			} else {
				existing := tokenTransport
				tokenTransport = func(rt netHTTP.RoundTripper) netHTTP.RoundTripper {
					return logMiddleware(existing(rt))
				}
			}
		}
	}

	return tokenTransport
}

func harCollectorMiddleware(ctx any, feature string, explicit *har.Collector) middlewares.Middleware {
	level := logger.Info
	if c, ok := ctx.(observabilityContext); ok {
		level, _ = c.EffectiveHARLevel(feature)
	}
	if explicit != nil && level < logger.Debug {
		level = logger.Trace
	}

	collector := effectiveHARCollector(ctx, feature, explicit)
	if collector == nil || level < logger.Debug {
		return nil
	}
	if level >= logger.Trace {
		return collector.Middleware()
	}
	return metadataHARMiddleware(collector)
}

func httpLoggerWithContent(rt netHTTP.RoundTripper, headers, bodies bool) netHTTP.RoundTripper {
	if !headers {
		return rt
	}

	l := &httpretty.Logger{
		Time:            true,
		TLS:             true,
		Auth:            true,
		RequestHeader:   true,
		RequestBody:     bodies,
		ResponseHeader:  true,
		ResponseBody:    bodies,
		Colors:          true,
		Formatters:      []httpretty.Formatter{&httpretty.JSONFormatter{}},
		MaxResponseBody: int64(properties.Int(4*1024, "http.log.response.body.length")),
	}
	l.SkipHeader(logger.SensitiveHeaders)
	return l.RoundTripper(rt)
}

// metadataHARMiddleware records request/response metadata (no bodies) into the
// collector, used when the HAR level is Debug but below Trace.
func metadataHARMiddleware(collector *har.Collector) middlewares.Middleware {
	return func(next netHTTP.RoundTripper) netHTTP.RoundTripper {
		return middlewares.RoundTripperFunc(func(req *netHTTP.Request) (*netHTTP.Response, error) {
			started := time.Now()
			entry := &har.Entry{
				StartedDateTime: started.UTC().Format(time.RFC3339),
				Request: har.Request{
					Method:      req.Method,
					URL:         req.URL.String(),
					HTTPVersion: harHTTPVersion(req.Proto),
					Cookies:     []har.Cookie{},
					Headers:     toHARHeaders(logger.SanitizeHeaders(req.Header)),
					QueryString: toHARQueryString(req.URL.Query()),
					HeadersSize: -1,
					BodySize:    -1,
				},
			}

			waitStart := time.Now()
			resp, err := next.RoundTrip(req)
			waitMs := float64(time.Since(waitStart).Microseconds()) / 1000.0

			entry.Timings = har.Timings{Wait: waitMs}
			entry.Time = waitMs
			if resp != nil {
				entry.Response = har.Response{
					Status:      resp.StatusCode,
					StatusText:  resp.Status,
					HTTPVersion: harHTTPVersion(resp.Proto),
					Cookies:     []har.Cookie{},
					Headers:     toHARHeaders(logger.SanitizeHeaders(resp.Header)),
					Content:     har.Content{Size: -1},
					RedirectURL: "",
					HeadersSize: -1,
					BodySize:    -1,
				}
			} else {
				entry.Response = har.Response{
					Cookies:     []har.Cookie{},
					Headers:     []har.Header{},
					Content:     har.Content{Size: -1},
					HeadersSize: -1,
					BodySize:    -1,
				}
			}

			collector.Add(entry)
			return resp, err
		})
	}
}

func toHARHeaders(h netHTTP.Header) []har.Header {
	headers := make([]har.Header, 0, len(h))
	for name, vals := range h {
		for _, v := range vals {
			headers = append(headers, har.Header{Name: name, Value: v})
		}
	}
	return headers
}

func toHARQueryString(q url.Values) []har.QueryString {
	qs := make([]har.QueryString, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			qs = append(qs, har.QueryString{Name: k, Value: v})
		}
	}
	return qs
}

func harHTTPVersion(proto string) string {
	if strings.TrimSpace(proto) == "" {
		return "HTTP/1.1"
	}
	return proto
}
