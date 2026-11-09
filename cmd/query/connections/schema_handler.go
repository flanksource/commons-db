package connections

import (
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query/schema"
)

// newConnectionSchemaHandler answers a schema request (`Accept:
// application/schema+json` or `?__schema`) on {prefix}/connection[s] with the
// connection form schema, so every host that mounts Service.Handler serves the
// form without re-implementing it, narrowed to policy. Every other request
// goes to next.
func newConnectionSchemaHandler(prefix string, service *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if !profiles.WantsSchema(r) || (path != prefix+"/connection" && path != prefix+"/connections") {
			next.ServeHTTP(w, r)
			return
		}
		form := schema.Connection(service.policy.schemaOptions())
		storage := service.Storage(r.Context())
		names, labels := []string{}, map[string]string{}
		for _, layer := range storage.Layers {
			if layer.Writable {
				names = append(names, layer.Name)
				labels[layer.Name] = layer.Label
			}
		}
		if len(names) > 0 {
			form["properties"].(schema.Schema)["layer"] = schema.Schema{"type": "string", "title": "Save to", "enum": names, "x-enum-labels": labels, "default": storage.DefaultLayer, "x-clicky-submit-target": true, "description": "Moving a connection preserves its credentials and removes it from its previous storage layer."}
		}
		body, err := schema.JSON(form)
		if err != nil {
			entity.WriteError(w, entity.NewStatusErrorf(http.StatusInternalServerError, "schema_failed",
				"encode the connection schema: %v", err))
			return
		}
		w.Header().Set("Content-Type", profiles.SchemaContentType)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}
