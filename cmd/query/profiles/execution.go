package profiles

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
		if name, ok := h.connectionProfileName(r.URL.Path); ok {
			h.mapConnection(w, r, name)
			return
		}
	}
	if r.Method == http.MethodGet && !wantsSchema(r) && !wantsLookup(r) {
		if name, ok := h.profileName(r.URL.Path); ok {
			h.execute(w, r, name)
			return
		}
	}
	// A selection too large for a query string is POSTed as a body. Ad-hoc
	// sampling is a different POST on a sibling path, so it is excluded by name
	// rather than swallowed here.
	if r.Method == http.MethodPost {
		if name, ok := h.profileName(r.URL.Path); ok && name != sampleProfileName {
			h.execute(w, r, name)
			return
		}
	}
	if r.Method == http.MethodOptions {
		if name, ok := h.profileName(r.URL.Path); ok && name != sampleProfileName {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	h.next.ServeHTTP(w, r)
}

func (h *execHandler) connectionProfileName(path string) (string, bool) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[0] != "profile" || parts[1] == "" || parts[2] != "connection" {
		return "", false
	}
	return parts[1], true
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
	return name, true
}

func (h *execHandler) execute(w http.ResponseWriter, r *http.Request, name string) {
	base, cancel := stdcontext.WithCancel(h.ctx.Context)
	defer cancel()
	go func() {
		select {
		case <-r.Context().Done():
			cancel()
		case <-base.Done():
		}
	}()
	execCtx := h.ctx.Wrap(base)

	resolved, err := Resolve(r.Context(), h.store, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	p := resolved.Profile
	if p.Provider.Type == "opentelemetry" && p.Provider.Connection == "" {
		h.writeConnectionRequired(w, name, resolved.ConnectionProfile)
		return
	}

	params, err := executeParams(r, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	export, err := parseExportRequest(r, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if export.scope == "all" && export.format == "clicky-json" {
		http.Error(w, "clicky-json is an interactive page format; choose an export format", http.StatusUnprocessableEntity)
		return
	}
	if export.scope == "all" && len(p.Columns) == 0 && isTabularExport(export.format) {
		http.Error(w, "all-row tabular exports require declared profile columns", http.StatusUnprocessableEntity)
		return
	}

	export.maxRows = p.RowLimits().MaxExportRows
	response, err := exportRows(execCtx, p, params, export)
	if err != nil {
		// A stale cursor is the caller's to fix by starting the walk again, so
		// it is a 400 rather than a 500 — and it says which input moved.
		if errors.Is(err, query.ErrCursorStale) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if export.format == "clicky-json" {
		page, err := query.CollectRows(response.rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filterKeys, err := p.ColumnFilterKeys()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		output, err := (&query.Result{Profile: p.Name, Rows: page, ColumnFilterKeys: filterKeys}).Render(p.Columns, "clicky-json")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		setExportHeaders(w, r, p.Name, export, response)
		_, _ = w.Write([]byte(output))
		return
	}

	columns, err := query.ClickyColumns(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	clickyRows := newProfileClickyRows(response.rows, columns, response.ceiling)
	opts := formatters.StreamOptions{Format: export.format}
	if export.format == "pdf" {
		opts.MaxRows = maxPDFRows
		var output bytes.Buffer
		if _, err := formatters.WriteTableStream(r.Context(), &output, clickyRows, opts); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
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
		format:  requestedFormat(r),
		scope:   r.URL.Query().Get("scope"),
		limit:   limits.PageSize,
		maxRows: limits.MaxExportRows,
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
	if value := r.URL.Query().Get(profile.ParamNameForRole(query.ParamRoleCursor, "cursor")); value != "" {
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
		for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
			switch strings.ToLower(strings.TrimSpace(strings.Split(part, ";")[0])) {
			case "application/json+clicky", "application/clicky+json":
				return "clicky-json"
			case "application/x-ndjson", "application/ndjson":
				return "ndjson"
			case "application/yaml", "application/x-yaml", "text/yaml":
				return "yaml"
			case "text/csv", "application/csv":
				return "csv"
			case "text/markdown":
				return "markdown"
			case "text/html":
				return "html"
			case "application/pdf":
				return "pdf"
			case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
				return "excel"
			}
		}
		return "json"
	default:
		return format
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
	case "format", "scope", "page", "limit", "offset", "filename", "_download", "args", "__schema", "__lookup", "__lookup_filter", "__lookup_q":
		return true
	default:
		return false
	}
}

func reservedParam(key string) bool { return IsReservedParam(key) }

func wantsLookup(r *http.Request) bool {
	return r.URL.Query().Get("__lookup") == "filters"
}
