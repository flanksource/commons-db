package profiles

import (
	"encoding/json"
	"net/http"

	"github.com/flanksource/commons-db/query"
)

func (h *profileSampleHandler) mapping(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "mapping preview requires POST", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request struct {
		Profile query.Profile `json:"profile"`
		Rows    []query.Row   `json:"rows"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := h.ctx.Wrap(r.Context()).WithTimeout(profileSampleTimeout)
	defer cancel()
	result, err := query.PreviewMapping(ctx, request.Profile, request.Rows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
