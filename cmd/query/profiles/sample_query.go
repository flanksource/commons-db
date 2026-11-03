package profiles

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/flanksource/commons-db/query/providers"
)

func (h *profileSampleHandler) translateQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "query translation requires POST", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var input providers.SpanQueryTranslation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "query translation requires one JSON object", http.StatusBadRequest)
		return
	}
	ctx, cancel := h.ctx.Wrap(r.Context()).WithTimeout(profileSampleTimeout)
	defer cancel()
	result, err := providers.TranslateSpanQuery(ctx, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
