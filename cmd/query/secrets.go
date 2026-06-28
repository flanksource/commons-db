package main

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// secretsHandler powers the connection form's cluster-backed pickers from the
// connection's LocalKubernetes client: the SecretKeySelector (Secrets/ConfigMaps
// and mid-masked value previews so the operator can tell which key holds the host
// vs the password — values are never returned in clear text), the NamespacePicker,
// and the WorkloadPicker. Selected secret references are persisted as
// `secret://<name>/<key>` / `configmap://<name>/<key>`; selected workloads as
// `svc://` / `ip://` / `proxy://` / `host://` URLs — both resolved at runtime
// against the connection's namespace.
//
//   - GET {prefix}/secrets?kind=secret|configmap[&namespace=]      -> [{name, keys}]
//   - GET {prefix}/secrets/preview?kind=&name=[&namespace=]        -> [{key, value}]
//   - GET {prefix}/namespaces                                      -> [name, ...]
//   - GET {prefix}/workloads?namespace=[&kinds=service,ingress,…]  -> {kind: [{name, ports, hosts}]}
type secretsHandler struct {
	prefix string
	kube   func() (kubernetes.Interface, error)
	next   http.Handler
}

// secretResource is a named Secret/ConfigMap and its data key names (no values).
type secretResource struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

// keyPreview is one key's mid-masked value preview.
type keyPreview struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func newSecretsHandler(prefix string, kube func() (kubernetes.Interface, error), next http.Handler) *secretsHandler {
	return &secretsHandler{prefix: strings.TrimRight(prefix, "/"), kube: kube, next: next}
}

func (h *secretsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), h.prefix), "/")
	switch rel {
	case "secrets", "secrets/preview", "namespaces", "workloads":
	default:
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet {
		h.next.ServeHTTP(w, r)
		return
	}

	client, err := h.kube()
	if err != nil {
		http.Error(w, fmt.Sprintf("kubernetes client unavailable: %v", err), http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	ns := namespaceOrDefault(q.Get("namespace"))

	var payload any
	switch rel {
	case "namespaces":
		payload, err = listNamespaces(r.Context(), client)
	case "workloads":
		payload, err = listWorkloads(r.Context(), client, ns, q.Get("kinds"))
	case "secrets":
		payload, err = listSecretResources(r.Context(), client, secretKind(q.Get("kind")), ns)
	case "secrets/preview":
		name := q.Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		payload, err = previewSecretKeys(r.Context(), client, secretKind(q.Get("kind")), name, ns)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeK8sJSON(w, payload)
}

// writeK8sJSON encodes a cluster-listing payload as CORS-enabled JSON.
func writeK8sJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(payload)
}

// listSecretResources returns the names and data key names of every Secret (or
// ConfigMap) in the namespace, sorted by name.
func listSecretResources(ctx gocontext.Context, client kubernetes.Interface, kind, ns string) ([]secretResource, error) {
	out := []secretResource{}
	if kind == "configmap" {
		list, err := client.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list configmaps in %q: %w", ns, err)
		}
		for _, cm := range list.Items {
			out = append(out, secretResource{Name: cm.Name, Keys: sortedKeys(cm.Data)})
		}
	} else {
		list, err := client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list secrets in %q: %w", ns, err)
		}
		for _, s := range list.Items {
			out = append(out, secretResource{Name: s.Name, Keys: sortedKeys(byteKeys(s.Data))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// previewSecretKeys returns a mid-masked preview for each key of the named
// resource so values are never exposed in clear text.
func previewSecretKeys(ctx gocontext.Context, client kubernetes.Interface, kind, name, ns string) ([]keyPreview, error) {
	var data map[string]string
	if kind == "configmap" {
		cm, err := client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get configmap %s/%s: %w", ns, name, err)
		}
		data = cm.Data
	} else {
		s, err := client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get secret %s/%s: %w", ns, name, err)
		}
		data = byteKeys(s.Data)
	}

	out := make([]keyPreview, 0, len(data))
	for _, k := range sortedKeys(data) {
		out = append(out, keyPreview{Key: k, Value: maskValue(data[k])})
	}
	return out, nil
}

// maskValue mid-masks a value: short/secret-like values are fully masked, longer
// values keep a head and tail so the operator can recognise a host or URL
// (e.g. "sql-server.example.com" -> "sql-••••.com").
func maskValue(v string) string {
	const bullet = "••••"
	r := []rune(strings.TrimSpace(v))
	switch {
	case len(r) == 0:
		return ""
	case len(r) <= 8:
		return bullet
	default:
		return string(r[:4]) + bullet + string(r[len(r)-4:])
	}
}

// secretKind normalises the kind query param to "secret" or "configmap".
func secretKind(s string) string {
	if s == "configmap" {
		return "configmap"
	}
	return "secret"
}

// namespaceOrDefault falls back to the "default" namespace when none is supplied.
func namespaceOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func byteKeys(m map[string][]byte) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}
