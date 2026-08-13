package profiles

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

// InfoContentType is the media type an info request negotiates. `?__info` says
// the same thing for a caller that cannot set a header — a browser following a
// link, or the download menu's own URL builder.
const InfoContentType = "application/info+json"

// wantsInfo reports whether this request asks what the URL would run instead of
// asking it to run.
func wantsInfo(r *http.Request) bool {
	if r.URL.Query().Has("__info") {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if mediaType == InfoContentType || mediaType == "application/json+info" {
			return true
		}
	}
	return false
}

// executionInfo is what a profile URL says about itself: the query its provider
// was sent, the arguments bound into it, how long it took and what came back.
//
// It is the answer to "what does this table actually run", which no view of the
// rows can give — the profile renders its query from params and filters that
// only exist at execution time, so nothing short of running it knows.
type executionInfo struct {
	Profile string `json:"profile"`
	// Provider is the registry key that ran, and Connection the connection it
	// ran against — reported only when the profile names one, because an inline
	// DSN carries credentials and this document is meant to be readable.
	Provider   string `json:"provider"`
	Connection string `json:"connection,omitempty"`

	// URL is the request this reports on, with the info marker removed: the
	// URL that produced the rows the caller is looking at.
	URL    string         `json:"url"`
	Params map[string]any `json:"params,omitempty"`

	// Mode, Rows and DurationMS describe the execution this request performed.
	// It is the same execution the URL would perform, minus rendering the rows,
	// so a profile that has to buffer its whole result to serve a page pays for
	// that here too — and says so.
	Mode       string  `json:"mode,omitempty"`
	Rows       int     `json:"rows"`
	DurationMS float64 `json:"durationMs"`

	// Order is the order the rows came back in, and DerivedOrder says it was
	// the provider's rather than the profile's.
	//
	// A derived order is the one thing about a page that is true of the result
	// and stated nowhere the author wrote it: the rows arrive sorted by a
	// column the profile never mentions. Reporting it here is what makes that
	// answerable without reading the provider's source.
	Order        query.Order `json:"order,omitempty"`
	DerivedOrder bool        `json:"derivedOrder,omitempty"`

	// Headers are the paging headers the GET would answer with — the total
	// above all, which is otherwise invisible to anyone reading a page.
	Headers     map[string]string          `json:"headers,omitempty"`
	Diagnostics *query.ProviderDiagnostics `json:"diagnostics,omitempty"`
}

// serveInfo runs the request the URL names for its diagnostics rather than its
// rows. The rows are read and counted so the provider's response details are
// complete, then discarded.
func (h *execHandler) serveInfo(w http.ResponseWriter, r *http.Request, name string) {
	setCORSHeaders(w)

	execCtx, cancel := h.executionContext(r)
	defer cancel()

	request, ok := h.resolveExecution(w, r, name)
	if !ok {
		return
	}
	p := request.profile

	// An info request explains the page in front of the caller, so it runs that
	// page — never the export behind it, which is the same query read to a
	// ceiling and would charge the whole result for one dialog.
	export := request.export
	export.scope = "page"
	export.format = "json"
	diagnostics := query.NewProviderDiagnostics(p.Provider.Type, p.Query, p.Provider.Options)
	export.diagnostics = diagnostics

	started := time.Now()
	response, err := exportRows(query.WithDiagnosticSink(execCtx, diagnostics), p, request.params, export)
	if err != nil {
		writeExecError(w, http.StatusBadRequest, "query_failed", query.WithDiagnostics(err, diagnostics))
		return
	}
	rows := 0
	for _, err := range response.rows {
		if err != nil {
			writeExecError(w, http.StatusBadRequest, "query_failed", query.WithDiagnostics(err, diagnostics))
			return
		}
		rows++
	}
	duration := time.Since(started)

	// Reported rather than recomputed from the profile's own order: this is the
	// order the rows above were actually cut by, so it is read from the same
	// place the execution read it.
	order, err := p.EffectiveOrder()
	if err != nil {
		writeExecError(w, http.StatusBadRequest, "query_failed", query.WithDiagnostics(err, diagnostics))
		return
	}

	// The GET's own headers, set by the GET's own code: an info request that
	// re-derived the total would be a second implementation of it.
	setExportHeaders(w, r, p.Name, export, response)
	w.Header().Set("Content-Type", InfoContentType)
	_ = json.NewEncoder(w).Encode(executionInfo{
		Profile:      p.Name,
		Provider:     p.Provider.Type,
		Connection:   profileConnectionReference(p.Provider.Connection),
		URL:          requestURLWithoutInfo(r),
		Params:       query.SanitizeDiagnosticValues(request.params),
		Mode:         response.mode,
		Rows:         rows,
		DurationMS:   float64(duration) / float64(time.Millisecond),
		Order:        order,
		DerivedOrder: len(p.Order) == 0 && len(order) > 0,
		Headers:      pagingHeaders(w.Header()),
		Diagnostics:  diagnostics.Snapshot(),
	})
}

// profileConnectionReference returns the connection a profile names, and
// nothing at all when it carries a DSN instead: an inline connection string is
// a credential, and this document is one a user is expected to read and share.
func profileConnectionReference(connection string) string {
	if strings.HasPrefix(connection, "connection://") {
		return connection
	}
	return ""
}

// requestURLWithoutInfo is the URL this request reports on — the one the table
// fetched, which is this one without the marker that turned it into a question.
func requestURLWithoutInfo(r *http.Request) string {
	url := *r.URL
	values := url.Query()
	values.Del("__info")
	url.RawQuery = values.Encode()
	return url.RequestURI()
}

func pagingHeaders(header http.Header) map[string]string {
	headers := map[string]string{}
	for key, values := range header {
		if strings.HasPrefix(key, "X-") && len(values) > 0 {
			headers[key] = values[0]
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}
