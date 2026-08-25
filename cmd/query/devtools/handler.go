package devtools

// The devtools HTTP surface. It is deliberately absent from the OpenAPI
// document: this is an operator tool whose shape is coupled to one client and
// will change with it, and publishing it would invite a generated client to
// depend on it. The connection browser, the secrets pickers and the session SSE
// endpoints are all unlisted for the same reason.

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/query"
)

// HandlerOptions configures New.
type HandlerOptions struct {
	Prefix string
	Store  *Store

	// Enabled gates the whole surface. A server told to hide error details is not
	// one that may hand out queries, headers and response bodies through a side
	// door, so the caller passes that decision in rather than this package
	// guessing at it.
	Enabled bool
}

type Handler struct {
	prefix  string
	store   *Store
	enabled bool
	next    http.Handler
}

// New builds the devtools handler. Wrap the rest of the chain with Handler so
// requests it does not own fall through unchanged.
func New(options HandlerOptions) *Handler {
	prefix := strings.TrimSuffix(options.Prefix, "/")
	if prefix == "" {
		prefix = "/api/v1"
	}
	return &Handler{prefix: prefix, store: options.Store, enabled: options.Enabled}
}

// Handler returns h wrapping next.
func (h *Handler) Handler(next http.Handler) http.Handler {
	h.next = next
	return h
}

// Store is the record store this handler serves, so the server can hand the same
// one to the arming middleware.
func (h *Handler) Store() *Store { return h.store }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root := h.prefix + "/devtools"
	if r.URL.Path != root && !strings.HasPrefix(r.URL.Path, root+"/") {
		if !h.enabled {
			h.next.ServeHTTP(w, r)
			return
		}
		// Arming happens on the way down and filing on the way back up, so the
		// record exists before the handler runs and is complete the moment it
		// returns — including for a streaming export, whose body is long finished
		// being written by then.
		writer, request, file, proceed := h.arm(w, r)
		if !proceed {
			return
		}
		if file != nil {
			defer file()
		}
		h.next.ServeHTTP(writer, request)
		return
	}
	if !h.enabled {
		http.NotFound(w, r)
		return
	}

	setDevtoolsCORS(w)
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, "+LevelHeader+", "+RefreshHeader+", Last-Event-ID")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, root), "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		h.serveCapabilities(w)
	case rest == "records" && r.Method == http.MethodGet:
		h.serveRecords(w, r)
	case rest == "records" && r.Method == http.MethodDelete:
		h.store.Clear()
		w.WriteHeader(http.StatusNoContent)
	case rest == "stream" && r.Method == http.MethodGet:
		h.serveStream(w, r)
	case rest == "logs" && r.Method == http.MethodGet:
		h.serveLogs(w, r)
	case rest == "inspection" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, InspectionCaches{Caches: inspection.Stats()})
	case rest == "inspection" && r.Method == http.MethodDelete:
		h.flushInspection(w, r)
	case strings.HasPrefix(rest, "records/") && r.Method == http.MethodGet:
		h.serveRecord(w, r, strings.TrimPrefix(rest, "records/"))
	default:
		http.Error(w, "no devtools route "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

// Capabilities tells a console what this server will do before it asks for it —
// which levels it accepts and what it will retain — so the UI is not built
// around constants duplicated on the other side of the wire.
type Capabilities struct {
	Enabled  bool     `json:"enabled"`
	Levels   []string `json:"levels"`
	Header   string   `json:"header"`
	Param    string   `json:"param"`
	IDHeader string   `json:"idHeader"`
	Stats    Stats    `json:"stats"`
}

func (h *Handler) serveCapabilities(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, Capabilities{
		Enabled:  true,
		Levels:   observability.SupportedLevelNames(),
		Header:   LevelHeader,
		Param:    LevelParam,
		IDHeader: IDHeader,
		Stats:    h.store.Stats(),
	})
}

// RecordsPage carries the history plus what the store has let go, so a console
// that finds a gap between its last sequence and the next can tell a quiet
// server from a full buffer.
type RecordsPage struct {
	Records []query.ExecutionSummary `json:"records"`
	Stats   Stats                    `json:"stats"`
}

func (h *Handler) serveRecords(w http.ResponseWriter, r *http.Request) {
	after, err := afterParam(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, RecordsPage{Records: h.store.Records(after), Stats: h.store.Stats()})
}

func (h *Handler) serveRecord(w http.ResponseWriter, r *http.Request, rest string) {
	id, wantsHAR := strings.CutSuffix(rest, "/har")
	if id == "" {
		http.Error(w, "record id is required", http.StatusBadRequest)
		return
	}
	detail, err := h.store.Detail(id)
	if err != nil {
		writeDetailError(w, id, err)
		return
	}
	if !wantsHAR {
		writeJSON(w, http.StatusOK, detail)
		return
	}
	if detail.HAR == nil {
		http.Error(w, "record "+id+" captured no HTTP traffic", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.har"`)
	_ = json.NewEncoder(w).Encode(detail.HAR)
}

// writeDetailError keeps "we no longer hold it" distinguishable from "no such
// record". Answering both with an empty document would tell a user their id was
// wrong when it was not.
func writeDetailError(w http.ResponseWriter, id string, err error) {
	var evicted *ErrDetailEvicted
	if !errors.As(err, &evicted) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusGone, map[string]any{
		"code": "detail_evicted", "id": id, "reason": evicted.Reason,
	})
}

// InspectionCaches is what the metadata caches currently hold.
//
// It is the other half of the Inspection tab. A record says what one request
// looked up; this says what the server is holding on everyone's behalf, which
// is the thing an operator flushes.
type InspectionCaches struct {
	Caches []inspection.CacheStats `json:"caches"`
}

// flushInspection drops cached metadata so the next lookup rebuilds it.
//
// Deliberately blunt and deliberately reported: a flush affects every request
// that follows, not just the caller's, so the response says exactly what went
// rather than answering 204 and leaving the operator to guess whether the entry
// they meant was even there.
func (h *Handler) flushInspection(w http.ResponseWriter, r *http.Request) {
	options := inspection.FlushOptions{
		Policy: strings.TrimSpace(r.URL.Query().Get("policy")),
		Key:    strings.TrimSpace(r.URL.Query().Get("key")),
		Prefix: strings.TrimSpace(r.URL.Query().Get("prefix")),
	}
	// An unknown policy is an error rather than a silent no-op: "nothing was
	// dropped" reads the same either way, and a typo would look like a cache
	// that refuses to clear.
	if options.Policy != "" && !slices.Contains(inspection.PolicyNames(), options.Policy) {
		http.Error(w, "unknown inspection cache "+strconv.Quote(options.Policy)+
			": expected one of "+strings.Join(inspection.PolicyNames(), ", "), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, inspection.Flush(options))
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request) {
	after, err := sse.LastEventSequence(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("format") == "ndjson" {
		sse.WriteNDJSON(w, "devtools-records.ndjson", h.store.Records(after))
		return
	}
	stream{records: h.store, after: after, keepalive: sse.DefaultKeepalive}.run(w, r)
}

func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	after, err := sse.LastEventSequence(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("format") == "ndjson" {
		sse.WriteNDJSON(w, "devtools-logs.ndjson", h.store.Logs(after))
		return
	}
	stream{records: h.store, after: after, keepalive: sse.DefaultKeepalive, logsOnly: true}.run(w, r)
}

func afterParam(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after"))
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		return 0, errAfter{raw}
	}
	return after, nil
}

type errAfter struct{ raw string }

func (e errAfter) Error() string {
	return "invalid after " + strconv.Quote(e.raw) + ": expected a record sequence"
}

func setDevtoolsCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", IDHeader)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
