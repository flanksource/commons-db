package profiles

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const profileSampleTimeout = 15 * time.Second

type profileSampleHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

type profileSampleRequest struct {
	Profile query.Profile  `json:"profile"`
	Params  map[string]any `json:"params,omitempty"`
}

func newProfileSampleHandler(prefix string, ctx dbcontext.Context, next http.Handler) *profileSampleHandler {
	return &profileSampleHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next}
}

func (h *profileSampleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.prefix+"/profile/sample" {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "profile sampling requires POST", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request profileSampleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid profile sample request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Profile.Name) == "" {
		request.Profile.Name = "sample"
	}
	ctx, cancel := h.ctx.WithTimeout(profileSampleTimeout)
	defer cancel()
	result, err := query.Sample(ctx.WithName("sample"), request.Profile, request.Params, query.DefaultSampleLimit)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, stdcontext.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
