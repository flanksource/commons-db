package profiles

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/flanksource/clicky/formatters"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

// maxPDFRows is a format ceiling, not a paging one: a PDF stops being readable
// long before the export ceiling. The page and export ceilings belong to the
// profile — query.RowLimits, resolved per request — which documents how the
// three differ.
const maxPDFRows = 1000

// execHandler serves profile execution and negotiated page/all-row exports at
// {prefix}/profile/{name}. Schema requests and every other path fall through to
// next.
type execHandler struct {
	prefix string
	ctx    dbcontext.Context
	store  Store
	next   http.Handler
}

func newExecHandler(prefix string, ctx dbcontext.Context, store Store, next http.Handler) *execHandler {
	return &execHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, store: store, next: next}
}

func (h *execHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		if name, ok := h.connectionProfileName(r.URL.EscapedPath()); ok {
			h.mapConnection(w, r, name)
			return
		}
	}
	// An info request asks the same URL what it would run rather than for its
	// rows, so it is answered before the execution branch reads it as one.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && wantsInfo(r) {
		if name, ok := h.profileName(r.URL.EscapedPath()); ok {
			if r.Method == http.MethodHead {
				w = headResponseWriter{w}
			}
			h.serveInfo(w, r, name)
			return
		}
	}
	// HEAD answers the same question as GET and is how a caller reads the paging
	// headers — the total above all — without paying for the rows. The body is
	// discarded rather than never produced: the totals it reports are only known
	// by running the page.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !wantsSchema(r) && !wantsLookup(r) {
		if name, ok := h.profileName(r.URL.EscapedPath()); ok {
			if r.Method == http.MethodHead {
				w = headResponseWriter{w}
			}
			h.execute(w, r, name)
			return
		}
	}
	// A selection too large for a query string is POSTed as a body. Ad-hoc
	// sampling is a different POST on a sibling path, so it is excluded by name
	// rather than swallowed here.
	if r.Method == http.MethodPost {
		if name, ok := h.profileName(r.URL.EscapedPath()); ok && name != sampleProfileName {
			h.execute(w, r, name)
			return
		}
	}
	if r.Method == http.MethodOptions {
		if name, ok := h.profileName(r.URL.EscapedPath()); ok && name != sampleProfileName {
			// Without the origin, the preflight fails and the request it was
			// clearing is never sent — so the methods allowed here would never
			// be reached.
			setCORSHeaders(w)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	h.next.ServeHTTP(w, r)
}

// headResponseWriter keeps the headers and drops the body, so a HEAD answers
// with exactly the metadata its GET would carry.
type headResponseWriter struct{ http.ResponseWriter }

func (headResponseWriter) Write(body []byte) (int, error) { return len(body), nil }

// Flush is preserved so a streaming export still runs to completion — its
// trailer and its ceiling are only resolved by reading to the end.
func (h headResponseWriter) Flush() {
	if flusher, ok := h.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h headResponseWriter) Unwrap() http.ResponseWriter { return h.ResponseWriter }

func (h *execHandler) connectionProfileName(path string) (string, bool) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[0] != "profile" || parts[1] == "" || parts[2] != "connection" {
		return "", false
	}
	name, err := url.PathUnescape(parts[1])
	return name, err == nil
}

// profileName returns the {name} segment of {prefix}/profile/{name}, or false.
func (h *execHandler) profileName(path string) (string, bool) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	if !strings.HasPrefix(rel, "profile/") {
		return "", false
	}
	name := strings.TrimPrefix(rel, "profile/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	name, err := url.PathUnescape(name)
	return name, err == nil
}

// executionRequest is one profile execution as the transport resolved it: the
// stored profile, the parameters the caller supplied, and the export asked for.
// Shared by the execution itself and by the info request that reports on it, so
// the two can never disagree about which query a URL names.
type executionRequest struct {
	profile query.Profile
	params  map[string]any
	export  exportRequest
}

// resolveExecution answers every question a profile URL raises before its rows
// are read. It writes its own failure response and reports false when it did.
func (h *execHandler) resolveExecution(w http.ResponseWriter, r *http.Request, name string) (executionRequest, bool) {
	name, err := h.storedProfileName(r.Context(), name)
	if err != nil {
		writeExecError(w, http.StatusNotFound, "profile_not_found", err)
		return executionRequest{}, false
	}
	resolved, err := Resolve(r.Context(), h.store, name)
	if err != nil {
		if errors.Is(err, dbcontext.ErrConnectionExpired) {
			writeExecError(w, http.StatusGone, "snapshot_expired", err)
			return executionRequest{}, false
		}
		writeExecError(w, http.StatusNotFound, "profile_not_found", err)
		return executionRequest{}, false
	}
	p := resolved.Profile
	if p.Provider.Type == "opentelemetry" && p.Provider.Connection == "" {
		h.writeConnectionRequired(w, name, resolved.ConnectionProfile)
		return executionRequest{}, false
	}

	params, err := executeParams(r, p)
	if err != nil {
		writeExecError(w, http.StatusBadRequest, "invalid_params", err)
		return executionRequest{}, false
	}

	export, err := parseExportRequest(r, p)
	if err != nil {
		writeExecError(w, http.StatusBadRequest, "invalid_export_request", err)
		return executionRequest{}, false
	}
	export.maxRows = p.RowLimits().MaxExportRows
	return executionRequest{profile: p, params: params, export: export}, true
}

// executionContext is the cancellable context an execution runs under: the
// server's, cancelled when the caller hangs up.
func (h *execHandler) executionContext(r *http.Request) (dbcontext.Context, stdcontext.CancelFunc) {
	base, cancel := stdcontext.WithCancel(h.ctx.Context)
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-base.Done():
		}
	}()
	return h.ctx.Wrap(base), cancel
}

func (h *execHandler) execute(w http.ResponseWriter, r *http.Request, name string) {
	// Before anything that can fail: every error below is one a browser has to
	// be able to read to act on.
	setCORSHeaders(w)

	execCtx, cancel := h.executionContext(r)
	defer cancel()

	request, ok := h.resolveExecution(w, r, name)
	if !ok {
		return
	}
	p, params, export := request.profile, request.params, request.export

	if export.scope == "all" && export.format == "clicky-json" {
		writeExecError(w, http.StatusUnprocessableEntity, "format_not_exportable",
			errors.New("clicky-json is an interactive page format; choose an export format"))
		return
	}
	// A tabular format flattens rows onto one column set, and a profile that
	// declares none has that set derived from the first row alone — so a key that
	// only appears later is dropped silently. The scope does not change that: a
	// page of a document-shaped backend is as heterogeneous as all of it. Refusing
	// is the only answer that does not hand back a file which looks complete.
	if len(p.Columns) == 0 && isTabularExport(export.format) {
		writeExecError(w, http.StatusUnprocessableEntity, "columns_required",
			errors.New("tabular exports require declared profile columns"))
		return
	}

	response, err := exportRows(execCtx, p, params, export)
	if err != nil {
		// A stale cursor is the caller's to fix by starting the walk again, so
		// it is a 400 rather than a 500 — and it says which input moved.
		if errors.Is(err, query.ErrCursorStale) {
			writeExecError(w, http.StatusBadRequest, "cursor_stale", err)
			return
		}
		writeExecError(w, http.StatusBadRequest, "query_failed", err)
		return
	}

	if export.format == "clicky-json" {
		page, err := query.CollectRows(response.rows)
		if err != nil {
			writeExecError(w, http.StatusInternalServerError, "query_failed", err)
			return
		}
		filterKeys, err := p.ColumnFilterKeys()
		if err != nil {
			writeExecError(w, http.StatusInternalServerError, "render_failed", err)
			return
		}
		output, err := (&query.Result{Profile: p.Name, Rows: page, ColumnFilterKeys: filterKeys}).Render(p.Columns, "clicky-json")
		if err != nil {
			writeExecError(w, http.StatusInternalServerError, "render_failed", err)
			return
		}
		setExportHeaders(w, r, p.Name, export, response)
		_, _ = w.Write([]byte(output))
		return
	}

	columns, err := query.ClickyColumns(p)
	if err != nil {
		writeExecError(w, http.StatusInternalServerError, "render_failed", err)
		return
	}
	clickyRows := newProfileClickyRows(response.rows, columns, response.ceiling)
	opts := formatters.StreamOptions{Format: export.format}
	if export.format == "pdf" {
		opts.MaxRows = maxPDFRows
		// A PDF's ceiling is its own and lower than the profile's, so the profile's
		// is not the number to report. Overshooting it is refused below rather
		// than truncated: a PDF that silently drops four fifths of its rows looks
		// exactly like a complete one.
		export.maxRows = maxPDFRows
		var output bytes.Buffer
		if _, err := formatters.WriteTableStream(r.Context(), &output, clickyRows, opts); err != nil {
			writeExecError(w, http.StatusUnprocessableEntity, "render_failed", err)
			return
		}
		// A PDF is buffered, so whether a ceiling bit is known before a byte is
		// written and belongs in the headers rather than a trailer.
		response.truncated = response.truncated || clickyRows.overflowed
		response.ceiling = 0
		setExportHeaders(w, r, p.Name, export, response)
		_, _ = w.Write(output.Bytes())
		return
	}

	setExportHeaders(w, r, p.Name, export, response)
	if _, err := formatters.WriteTableStream(r.Context(), w, clickyRows, opts); err != nil {
		// Headers may already be committed for a true stream; cancellation and
		// backend cursor errors therefore terminate the response at that point.
		execCtx.Warnf("profile %q export failed after streaming began: %v", p.Name, err)
		return
	}
	// Whether the ceiling bit is only knowable now, which is why it was
	// declared as a trailer: an export that stopped short and said nothing is
	// indistinguishable from one that finished.
	if clickyRows.overflowed {
		execCtx.Warnf("profile %q: export reached its %d row ceiling with more rows to come", p.Name, export.maxRows)
		w.Header().Set("X-Truncated", "true")
	}
}

func (h *execHandler) storedProfileName(ctx stdcontext.Context, name string) (string, error) {
	if !strings.HasPrefix(name, "profile-") {
		return name, nil
	}
	stored, err := h.store.List(ctx)
	if err != nil {
		return "", err
	}
	for _, profile := range stored {
		if profileSurfaceKey(profile.Name) == name {
			return profile.Name, nil
		}
	}
	return "", fmt.Errorf("profile surface %q not found", name)
}

// execError is the body every failed execution returns.
//
// The 409 that asks for a connection has always been structured, and a client
// that can branch on one code but has to string-match the rest is a client that
// breaks when a message is reworded. Code is the stable part; Message is for a
// person to read.
// Diagnostics is present when the failure carried the provider request that
// produced it — the statement, its bound arguments, the provider's own
// details. A message alone says a query failed; this says which one.
type execError struct {
	Code        string                     `json:"code"`
	Message     string                     `json:"message"`
	Diagnostics *query.ProviderDiagnostics `json:"diagnostics,omitempty"`
}

func writeExecError(w http.ResponseWriter, status int, code string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(execError{
		Code:        code,
		Message:     err.Error(),
		Diagnostics: query.DiagnosticsFromError(err),
	})
}

type connectionMappingRequest struct {
	Connection string `json:"connection"`
}

func (h *execHandler) mapConnection(w http.ResponseWriter, r *http.Request, name string) {
	resolved, err := Resolve(r.Context(), h.store, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if resolved.Profile.Provider.Type != models.ConnectionTypeOpenTelemetry {
		http.Error(w, fmt.Sprintf("profile %q has provider type %q, expected %q", name, resolved.Profile.Provider.Type, models.ConnectionTypeOpenTelemetry), http.StatusBadRequest)
		return
	}
	var request connectionMappingRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("decode connection mapping: %v", err), http.StatusBadRequest)
		return
	}
	request.Connection = strings.TrimSpace(request.Connection)
	if request.Connection == "" {
		http.Error(w, "connection is required", http.StatusBadRequest)
		return
	}
	selected, err := dbcontext.FindConnectionByURL(h.ctx, request.Connection)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if selected == nil {
		http.Error(w, fmt.Sprintf("connection %q not found", request.Connection), http.StatusBadRequest)
		return
	}
	if selected.Type != models.ConnectionTypeOpenTelemetry {
		http.Error(w, fmt.Sprintf("connection %q has type %q, expected %q", selected.Name, selected.Type, models.ConnectionTypeOpenTelemetry), http.StatusBadRequest)
		return
	}
	owner, err := h.store.Get(r.Context(), resolved.ConnectionProfile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner.Provider.Connection = request.Connection
	if err := h.store.Save(r.Context(), owner); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"profile": name, "mappingProfile": resolved.ConnectionProfile, "connection": request.Connection,
	})
}

func (h *execHandler) writeConnectionRequired(w http.ResponseWriter, profile, mappingProfile string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": "profile_connection_required", "profile": profile,
		"mappingProfile": mappingProfile, "connectionType": models.ConnectionTypeOpenTelemetry,
		"mappingUrl": h.prefix + "/profile/" + profile + "/connection",
	})
}

func parseExportRequest(r *http.Request, profile query.Profile) (exportRequest, error) {
	limits := profile.RowLimits()
	request := exportRequest{
		format:   requestedFormat(r),
		scope:    r.URL.Query().Get("scope"),
		limit:    limits.PageSize,
		maxRows:  limits.MaxExportRows,
		pageable: profile.Pageable() == nil,
		paging:   query.SupportsPaging(profile.Provider.Type),
	}
	if request.scope == "" {
		request.scope = "page"
	}
	if request.scope != "page" && request.scope != "all" {
		return request, fmt.Errorf("invalid export scope %q", request.scope)
	}
	if !supportedExportFormat(request.format) {
		return request, fmt.Errorf("unsupported export format %q", request.format)
	}
	limitParam := profile.ParamNameForRole(query.ParamRoleLimit, "limit")
	offsetParam := profile.ParamNameForRole(query.ParamRoleOffset, "offset")
	if value := r.URL.Query().Get(limitParam); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit <= 0 || limit > limits.MaxPageSize {
			return request, fmt.Errorf("limit must be between 1 and %d; export more with scope=all", limits.MaxPageSize)
		}
		request.limit = limit
	}
	if value := r.URL.Query().Get(offsetParam); value != "" {
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 {
			return request, fmt.Errorf("offset must be zero or greater")
		}
		request.offset = offset
	}
	if value := r.URL.Query().Get("cursor"); value != "" {
		request.cursor = query.Cursor(value)
	}
	if request.scope == "all" {
		request.offset = 0
		request.cursor = ""
	}
	if !request.cursor.IsZero() && request.offset != 0 {
		return request, fmt.Errorf("a cursor already says where to resume, so it cannot be combined with an offset")
	}
	return request, nil
}

func supportedExportFormat(format string) bool {
	switch format {
	case "clicky-json", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf":
		return true
	default:
		return false
	}
}

func requestedFormat(r *http.Request) string {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "xlsx":
		return "excel"
	case "md":
		return "markdown"
	case "yml":
		return "yaml"
	case "":
		return acceptedFormat(r.Header.Get("Accept"))
	default:
		return format
	}
}

// acceptedFormat picks the format an Accept header asks for.
//
// Accept is ranked, not ordered: a caller listing text/html first at q=0.1 and
// the clicky envelope at q=0.9 is asking for the envelope. Reading the first
// recognised entry answers with the one it weighted lowest — and a q=0 is a
// refusal, not a low preference. Ties keep the earlier entry, which is the
// order the caller wrote them in.
func acceptedFormat(accept string) string {
	best, bestQuality := "json", -1.0
	for _, part := range strings.Split(accept, ",") {
		fields := strings.Split(part, ";")
		format, ok := formatForMediaType(strings.ToLower(strings.TrimSpace(fields[0])))
		if !ok {
			continue
		}
		quality := 1.0
		for _, parameter := range fields[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || strings.ToLower(strings.TrimSpace(name)) != "q" {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				continue
			}
			quality = parsed
		}
		if quality <= 0 || quality <= bestQuality {
			continue
		}
		best, bestQuality = format, quality
	}
	return best
}

func formatForMediaType(media string) (string, bool) {
	switch media {
	case "application/json+clicky", "application/clicky+json":
		return "clicky-json", true
	case "application/x-ndjson", "application/ndjson":
		return "ndjson", true
	case "application/yaml", "application/x-yaml", "text/yaml":
		return "yaml", true
	case "text/csv", "application/csv":
		return "csv", true
	case "text/markdown":
		return "markdown", true
	case "text/html":
		return "html", true
	case "application/pdf":
		return "pdf", true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "excel", true
	case "application/json":
		return "json", true
	default:
		return "", false
	}
}

func isTabularExport(format string) bool {
	switch format {
	case "csv", "markdown", "html", "excel", "pdf":
		return true
	default:
		return false
	}
}

func exportContentType(format string) string {
	switch format {
	case "clicky-json":
		return "application/json+clicky"
	case "json":
		return "application/json"
	case "ndjson":
		return "application/x-ndjson"
	case "yaml":
		return "application/yaml"
	case "csv":
		return "text/csv; charset=utf-8"
	case "markdown":
		return "text/markdown; charset=utf-8"
	case "html":
		return "text/html; charset=utf-8"
	case "excel":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func exportExtension(format string) string {
	switch format {
	case "markdown":
		return ".md"
	case "excel":
		return ".xlsx"
	case "ndjson":
		return ".ndjson"
	default:
		return "." + format
	}
}

func sanitizeExportFilename(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	parts := strings.Split(filename, "/")
	filename = parts[len(parts)-1]
	filename = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`\";`, r) {
			return '_'
		}
		return r
	}, filename)
	filename = strings.Trim(filename, " .")
	if filename == "" {
		return "query-export.json"
	}
	return filename
}

// reservedParam reports whether a query-string key is a transport concern (paging,
// format, content-negotiation) rather than a profile filter param.
func IsReservedParam(key string) bool {
	switch key {
	case "format", "scope", "page", "limit", "offset", "filename", "_download", "args", "__schema", "__info", "__lookup", "__lookup_filter", "__lookup_q":
		return true
	default:
		return false
	}
}

func reservedParam(key string) bool { return IsReservedParam(key) }

func wantsLookup(r *http.Request) bool {
	return r.URL.Query().Get("__lookup") == "filters"
}
