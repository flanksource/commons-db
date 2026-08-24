package connections

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/kubecatalog"
	"github.com/flanksource/commons-db/models"
	"k8s.io/client-go/kubernetes"
)

func (h *connectionBrowserHandler) kubernetesCatalogClient(
	r *http.Request,
	connection *models.Connection,
) (kubernetes.Interface, error) {
	if connection.Type != models.ConnectionTypeKubernetes {
		return nil, fmt.Errorf("connection type %q does not provide a Kubernetes catalog", connection.Type)
	}
	client, err := h.kubernetesClient(r.Context(), connection)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("Kubernetes connection %q returned no client", connection.Name)
	}
	return client, nil
}

func (h *connectionBrowserHandler) serveKubernetesNamespaces(
	w http.ResponseWriter,
	r *http.Request,
	connection *models.Connection,
) {
	client, err := h.kubernetesCatalogClient(r, connection)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	namespaces, err := kubecatalog.ListNamespaces(r.Context(), client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, namespaces)
}

func (h *connectionBrowserHandler) serveKubernetesWorkloads(
	w http.ResponseWriter,
	r *http.Request,
	connection *models.Connection,
) {
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}
	client, err := h.kubernetesCatalogClient(r, connection)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	workloads, err := kubecatalog.ListWorkloads(
		r.Context(),
		client,
		namespace,
		r.URL.Query().Get("kinds"),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, workloads)
}
