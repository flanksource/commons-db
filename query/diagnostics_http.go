package query

import (
	"net/http"
	"strings"
)

// HTTPTransport wraps base so the exchange carrying this run's provider request
// is recorded: where it went, what headers it took with it, and what the
// backend answered.
//
// Exactly one exchange is recorded — the first after RecordRequest, which is
// the statement the provider has just described. Everything a client does on
// the way there, a ping or a mapping lookup or a token fetch, happens before
// that and is left alone; a walk's later pages go to the same endpoint with the
// same headers and differ only in the body already reported.
//
// It returns base unchanged for an ordinary run, which has no diagnostics to
// record into and must not pay to be watched.
func (d *ProviderDiagnostics) HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if d == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &diagnosticTransport{diagnostics: d, base: base}
}

type diagnosticTransport struct {
	diagnostics *ProviderDiagnostics
	base        http.RoundTripper
}

func (t *diagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !t.diagnostics.takeArmed() {
		return t.base.RoundTrip(request)
	}
	response, err := t.base.RoundTrip(request)
	// Read after the call rather than before it: the connection layer sets the
	// credential on this same request on its way down, so the headers are only
	// the ones that went on the wire once the call has returned.
	t.diagnostics.recordHTTP(request, response)
	return response, err
}

// takeArmed reports whether this exchange is the one the provider just
// described, and disarms so the next one is not mistaken for it.
func (d *ProviderDiagnostics) takeArmed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.armed {
		return false
	}
	d.armed = false
	return true
}

func (d *ProviderDiagnostics) recordHTTP(request *http.Request, response *http.Response) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Request.Method = request.Method
	d.Request.URL = redactDiagnosticURL(request.URL.String())
	d.Request.Headers = diagnosticHeaders(request.Header)
	if response == nil {
		return
	}
	d.Response.Status = response.StatusCode
	d.Response.Headers = diagnosticHeaders(response.Header)
}

// diagnosticHeaders flattens headers to one value per name and masks the ones
// that carry a credential: that an Authorization header was sent is worth
// reporting, and its contents are worth nothing to the person reading this.
func diagnosticHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	headers := make(map[string]string, len(header))
	for name, values := range header {
		if diagnosticSecretKey(name) {
			headers[name] = "********"
			continue
		}
		headers[name] = strings.Join(values, ", ")
	}
	return headers
}

func cloneDiagnosticHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}
