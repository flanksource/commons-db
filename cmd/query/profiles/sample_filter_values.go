package profiles

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const sampleFilterValuesPath = "/profile/" + sampleProfileName + "/filters/values"

type profileSampleFilterValuesHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

type profileSampleFilterValuesRequest struct {
	Profile           query.Profile     `json:"profile"`
	Params            map[string]any    `json:"params,omitempty"`
	Filters           map[string]string `json:"filters,omitempty"`
	FilterColumns     []query.ColumnDef `json:"filterColumns"`
	FilterKey         string            `json:"filterKey"`
	Search            string            `json:"search,omitempty"`
	Limit             int               `json:"limit,omitempty"`
	RefreshInspection bool              `json:"refreshInspection,omitempty"`
}

type profileSampleFilterOption struct {
	Value string `json:"value"`
	Count int64  `json:"count,omitempty"`
}

type profileSampleFilterValuesResponse struct {
	Options       []profileSampleFilterOption `json:"options"`
	Total         int64                       `json:"total,omitempty"`
	TotalRelation string                      `json:"totalRelation"`
	Truncated     bool                        `json:"truncated,omitempty"`
}

func newProfileSampleFilterValuesHandler(
	prefix string,
	ctx dbcontext.Context,
	next http.Handler,
) *profileSampleFilterValuesHandler {
	return &profileSampleFilterValuesHandler{
		prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next,
	}
}

func (h *profileSampleFilterValuesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.prefix+sampleFilterValuesPath {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeSampleError(w, http.StatusMethodNotAllowed, sampleError(h.ctx, fmt.Errorf("profile sample filter lookup requires POST")))
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request profileSampleFilterValuesRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeSampleError(w, http.StatusBadRequest, sampleError(h.ctx, fmt.Errorf("invalid profile sample filter request: %w", err)))
		return
	}
	if strings.TrimSpace(request.FilterKey) == "" {
		writeSampleError(w, http.StatusBadRequest, sampleError(h.ctx, fmt.Errorf("filterKey is required")))
		return
	}
	if request.Limit <= 0 {
		request.Limit = 20
	}
	if request.Limit > query.MaxFilterLookupLimit {
		writeSampleError(w, http.StatusBadRequest, sampleError(h.ctx, fmt.Errorf("filter lookup limit must be at most %d", query.MaxFilterLookupLimit)))
		return
	}
	if strings.TrimSpace(request.Profile.Name) == "" {
		request.Profile.Name = sampleProfileName
	}
	ctx, cancel := h.ctx.WithTimeout(profileSampleTimeout)
	defer cancel()
	ctx = devtools.WithRequestRecorder(ctx, r)
	options, total, err := query.SampleFilterValues(ctx.WithName("sample-filter-values"), request.Profile, query.SampleFilterValuesOptions{
		Params: request.Params, Filters: request.Filters, FilterColumns: request.FilterColumns,
		FilterKey: request.FilterKey, Search: request.Search, Limit: request.Limit,
		Inspection: query.InspectionOptions{Refresh: request.RefreshInspection},
	})
	if err != nil {
		writeSampleError(w, http.StatusUnprocessableEntity, sampleError(ctx, err))
		return
	}
	response := profileSampleFilterValuesResponse{
		Options: make([]profileSampleFilterOption, 0, len(options)), TotalRelation: total.Relation(),
	}
	if total != nil {
		response.Total = total.Value
		response.Truncated = total.Value > int64(len(options))
	}
	for _, option := range options {
		response.Options = append(response.Options, profileSampleFilterOption{Value: option.Value, Count: option.Count})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
