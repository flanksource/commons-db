package profiles

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/samber/oops"
)

const profileSampleTimeout = 15 * time.Second

// sampleProfileName is the {name} segment the ad-hoc sample handler owns. The
// execute handler excludes it by name so a POST there is not read as a run of a
// stored profile called "sample".
const sampleProfileName = "sample"

type profileSampleHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

type profileSampleRequest struct {
	Profile           query.Profile           `json:"profile"`
	Params            map[string]any          `json:"params,omitempty"`
	Filters           map[string]string       `json:"filters,omitempty"`
	FilterColumns     []query.ColumnDef       `json:"filterColumns,omitempty"`
	Pagination        samplePaginationRequest `json:"pagination,omitempty"`
	PreviewProcessors bool                    `json:"previewProcessors,omitempty"`
	RefreshInspection bool                    `json:"refreshInspection,omitempty"`
}

type samplePaginationRequest struct {
	Limit  int          `json:"limit,omitempty"`
	Cursor query.Cursor `json:"cursor,omitempty"`
}

func newProfileSampleHandler(prefix string, ctx dbcontext.Context, next http.Handler) *profileSampleHandler {
	return &profileSampleHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next}
}

func (h *profileSampleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.prefix+"/profile/"+sampleProfileName {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeSampleError(
			w,
			http.StatusMethodNotAllowed,
			sampleError(h.ctx, errors.New("profile sampling requires POST")),
		)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request profileSampleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeSampleError(w, http.StatusBadRequest, sampleError(h.ctx, fmt.Errorf("invalid profile sample request: %w", err)))
		return
	}
	if strings.TrimSpace(request.Profile.Name) == "" {
		request.Profile.Name = "sample"
	}
	ctx, cancel := h.ctx.WithTimeout(profileSampleTimeout)
	defer cancel()
	// How much this sample explains about itself is the console's decision, sent
	// as the arming header — the profile builder no longer asks for diagnostics
	// with a flag of its own, so what it shows and what the console shows are
	// the same capture rather than two runs that agree by luck.
	ctx = devtools.WithRequestRecorder(ctx, r)
	result, err := query.Sample(ctx.WithName("sample"), request.Profile, query.SampleOptions{
		Params: request.Params, Filters: request.Filters, FilterColumns: request.FilterColumns,
		Page: query.PageRequest{
			Limit: request.Pagination.Limit, Cursor: request.Pagination.Cursor,
		},
		PreviewProcessors: request.PreviewProcessors,
		Inspection:        query.InspectionOptions{Refresh: request.RefreshInspection},
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, stdcontext.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeSampleError(w, status, sampleError(ctx, err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func sampleError(ctx dbcontext.Context, err error) error {
	return ctx.Oops().With("operation", "profile.sample").Wrap(err)
}

func writeSampleError(w http.ResponseWriter, status int, err error) {
	payload := map[string]any{"error": err.Error()}
	if oopsError, ok := oops.AsOops(err); ok {
		payload = oopsError.ToMap()
	}
	payload["diagnostics"] = query.DiagnosticsFromError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
