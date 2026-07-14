package profiles

import (
	"bytes"
	stdcontext "context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/formatters"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const (
	defaultPageLimit = 100
	maxPageLimit     = 1000
	maxPDFRows       = 1000
)

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
	if r.Method == http.MethodGet && !wantsSchema(r) {
		if name, ok := h.profileName(r.URL.Path); ok {
			h.execute(w, r, name)
			return
		}
	}
	h.next.ServeHTTP(w, r)
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
	execCtx := dbcontext.NewContext(base)

	p, err := h.store.Get(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	params := map[string]any{}
	for k, vs := range r.URL.Query() {
		if reservedParam(k) || p.HasParamRoleName(query.ParamRoleLimit, k) || p.HasParamRoleName(query.ParamRoleOffset, k) || len(vs) == 0 {
			continue
		}
		params[k] = vs[0]
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

	rows, total, mode, err := h.exportRows(execCtx, p, params, export)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if export.format == "clicky-json" {
		defer rows.Close()
		page, err := drainRows(rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		output, err := (&query.Result{Profile: p.Name, Rows: page}).Render(p.Columns, "clicky-json")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		setExportHeaders(w, r, p.Name, export, mode, total)
		_, _ = w.Write([]byte(output))
		return
	}

	clickyRows := &profileClickyRows{rows: rows, columns: query.ClickyColumns(p.Columns)}
	opts := formatters.StreamOptions{Format: export.format}
	if export.format == "pdf" {
		opts.MaxRows = maxPDFRows
		var output bytes.Buffer
		if _, err := formatters.WriteTableStream(r.Context(), &output, clickyRows, opts); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		setExportHeaders(w, r, p.Name, export, mode, total)
		_, _ = w.Write(output.Bytes())
		return
	}

	setExportHeaders(w, r, p.Name, export, mode, total)
	if _, err := formatters.WriteTableStream(r.Context(), w, clickyRows, opts); err != nil {
		// Headers may already be committed for a true stream; cancellation and
		// backend cursor errors therefore terminate the response at that point.
		execCtx.Warnf("profile %q export failed after streaming began: %v", p.Name, err)
	}
}

type exportRequest struct {
	format string
	scope  string
	limit  int
	offset int
}

func parseExportRequest(r *http.Request, profile query.Profile) (exportRequest, error) {
	request := exportRequest{format: requestedFormat(r), scope: r.URL.Query().Get("scope"), limit: defaultPageLimit}
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
		if err != nil || limit <= 0 || limit > maxPageLimit {
			return request, fmt.Errorf("limit must be between 1 and %d", maxPageLimit)
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
	if request.scope == "all" {
		request.offset = 0
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

func (h *execHandler) exportRows(ctx dbcontext.Context, p query.Profile, params map[string]any, request exportRequest) (query.RowIterator, *int, string, error) {
	bufferedPipeline := len(p.Processors) > 0 || p.Top != nil
	if request.scope == "all" {
		if !supportsAllRows(p.Provider.Type) {
			return nil, nil, "", fmt.Errorf("provider %q does not support all-row export", p.Provider.Type)
		}
		if bufferedPipeline {
			result, err := query.Execute(ctx, p, params)
			if err != nil {
				return nil, nil, "", err
			}
			total := len(result.Rows)
			return query.SliceRows(result.Rows), &total, "buffered", nil
		}
		rows, err := query.ExecuteRows(ctx, p, params)
		return rows, nil, "streaming", err
	}

	if !bufferedPipeline && query.SupportsStreaming(p.Provider.Type) {
		maxRows := request.offset + request.limit + 1
		if maxRows <= request.offset {
			return nil, nil, "", fmt.Errorf("requested page is too large")
		}
		rows, err := query.ExecuteRowsBounded(ctx, p, maxRows, params)
		if err != nil {
			return nil, nil, "", err
		}
		page, complete, err := collectPage(rows, request.offset, request.limit)
		if err != nil {
			return nil, nil, "", err
		}
		var total *int
		if complete {
			value := request.offset + len(page)
			total = &value
		}
		return query.SliceRows(page), total, "page", nil
	}

	result, err := query.Execute(ctx, p, params)
	if err != nil {
		return nil, nil, "", err
	}
	total := len(result.Rows)
	start := min(request.offset, total)
	end := min(start+request.limit, total)
	return query.SliceRows(result.Rows[start:end]), &total, "page", nil
}

func collectPage(rows query.RowIterator, offset, limit int) ([]query.Row, bool, error) {
	defer rows.Close()
	for skipped := 0; skipped < offset && rows.Next(); skipped++ {
	}
	page := make([]query.Row, 0, limit)
	for len(page) < limit && rows.Next() {
		page = append(page, rows.Row())
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	complete := !rows.Next()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return page, complete, nil
}

func drainRows(rows query.RowIterator) ([]query.Row, error) {
	var result []query.Row
	for rows.Next() {
		result = append(result, rows.Row())
	}
	return result, rows.Err()
}

type profileClickyRows struct {
	rows    query.RowIterator
	columns []api.ColumnDef
}

func (i *profileClickyRows) Columns() []api.ColumnDef { return i.columns }
func (i *profileClickyRows) Next() bool               { return i.rows.Next() }
func (i *profileClickyRows) Row() map[string]any      { return i.rows.Row() }
func (i *profileClickyRows) Err() error               { return i.rows.Err() }
func (i *profileClickyRows) Close() error             { return i.rows.Close() }

func supportsAllRows(provider string) bool {
	return query.SupportsStreaming(provider)
}

func isTabularExport(format string) bool {
	switch format {
	case "csv", "markdown", "html", "excel", "pdf":
		return true
	default:
		return false
	}
}

func setExportHeaders(w http.ResponseWriter, r *http.Request, profileName string, request exportRequest, mode string, total *int) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, X-Export-Mode, X-Page-Limit, X-Page-Offset, X-Total-Count")
	w.Header().Set("Content-Type", exportContentType(request.format))
	w.Header().Set("X-Export-Mode", mode)
	if request.scope == "page" {
		w.Header().Set("X-Page-Limit", strconv.Itoa(request.limit))
		w.Header().Set("X-Page-Offset", strconv.Itoa(request.offset))
	}
	if total != nil {
		w.Header().Set("X-Total-Count", strconv.Itoa(*total))
	}
	if r.URL.Query().Has("_download") || r.URL.Query().Get("filename") != "" {
		filename := r.URL.Query().Get("filename")
		if filename == "" {
			filename = profileName + exportExtension(request.format)
		}
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(sanitizeExportFilename(filename)))
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
