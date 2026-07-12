package main

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// helmReleaseSecretType is the Secret type Helm writes its release state to.
const helmReleaseSecretType = "helm.sh/release.v1"

// secretsHandler powers the connection form's cluster-backed pickers from the
// connection's LocalKubernetes client: the SecretKeySelector (Secrets/ConfigMaps
// and mid-masked value previews so the operator can tell which key holds the host
// vs the password — values are never returned in clear text), the NamespacePicker,
// and the WorkloadPicker. Selected secret references are persisted as
// `secret://<name>/<key>` / `configmap://<name>/<key>`; selected workloads as
// `svc://` / `ip://` / `proxy://` / `host://` URLs — both resolved at runtime
// against the connection's namespace.
//
//   - GET {prefix}/secrets?kind=secret|configmap|helm|serviceaccount[&namespace=] -> [{name, keys}]
//   - GET {prefix}/secrets/preview?kind=&name=[&namespace=]        -> [{key, value}]
//   - GET {prefix}/namespaces                                      -> [name, ...]
//   - GET {prefix}/workloads?namespace=[&kinds=service,ingress,…]  -> {kind: [{name, ports, hosts}]}
//
// The helm and serviceaccount kinds list names only (no keys): a Helm value is
// addressed by a freeform jsonpath key and a service-account reference has no
// key. A helm preview decodes the release's merged values to surface its
// top-level keys.
type secretsHandler struct {
	prefix string
	ctx    dbcontext.Context
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

func newSecretsHandler(prefix string, ctx dbcontext.Context, kube func() (kubernetes.Interface, error), next http.Handler) *secretsHandler {
	return &secretsHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, kube: kube, next: next}
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
		payload, err = listSecretResources(r.Context(), client, q.Get("kind"), ns)
	case "secrets/preview":
		name := q.Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		payload, err = h.previewKeys(r.Context(), client, q.Get("kind"), name, ns)
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

// listSecretResources returns the resources of the requested kind in the
// namespace, sorted by name. Secrets and ConfigMaps carry their data key names;
// Helm releases and ServiceAccounts are name-only (their references carry no
// key, or a freeform jsonpath key).
func listSecretResources(ctx gocontext.Context, client kubernetes.Interface, kind, ns string) ([]secretResource, error) {
	out := []secretResource{}
	switch kind {
	case "configmap":
		list, err := client.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list configmaps in %q: %w", ns, err)
		}
		for _, cm := range list.Items {
			out = append(out, secretResource{Name: cm.Name, Keys: sortedKeys(cm.Data)})
		}
	case "helm":
		return listHelmReleases(ctx, client, ns)
	case "serviceaccount":
		list, err := client.CoreV1().ServiceAccounts(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list service accounts in %q: %w", ns, err)
		}
		for _, sa := range list.Items {
			out = append(out, secretResource{Name: sa.Name})
		}
	default:
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

// listHelmReleases returns the deployed Helm release names in the namespace,
// deduplicated across revisions (Helm keeps one release Secret per revision).
func listHelmReleases(ctx gocontext.Context, client kubernetes.Interface, ns string) ([]secretResource, error) {
	list, err := client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("type=%s", helmReleaseSecretType),
		LabelSelector: "status=deployed",
	})
	if err != nil {
		return nil, fmt.Errorf("list helm releases in %q: %w", ns, err)
	}
	seen := map[string]bool{}
	out := []secretResource{}
	for _, s := range list.Items {
		name := s.Labels["name"]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, secretResource{Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// previewKeys returns a mid-masked preview for each key of the named resource so
// values are never exposed in clear text. Helm decodes the release's merged
// values to surface its top-level keys; the other kinds read data keys directly.
func (h *secretsHandler) previewKeys(ctx gocontext.Context, client kubernetes.Interface, kind, name, ns string) ([]keyPreview, error) {
	if kind == "helm" {
		return h.previewHelmKeys(name, ns)
	}
	return previewSecretKeys(ctx, client, kind, name, ns)
}

// previewSecretKeys returns a mid-masked preview for each key of the named
// Secret or ConfigMap. Name-only kinds (serviceaccount) preview nothing.
func previewSecretKeys(ctx gocontext.Context, client kubernetes.Interface, kind, name, ns string) ([]keyPreview, error) {
	if kind == "serviceaccount" {
		return []keyPreview{}, nil
	}

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

// previewHelmKeys surfaces the top-level keys of a Helm release's merged values
// as jsonpath starting points, masking scalar values and rendering nested
// objects/arrays as a "{…}" / "[…]" hint.
func (h *secretsHandler) previewHelmKeys(name, ns string) ([]keyPreview, error) {
	merged, err := dbcontext.GetHelmValuesFromCache(h.ctx, ns, name)
	if err != nil {
		return nil, fmt.Errorf("decode helm release %s/%s: %w", ns, name, err)
	}
	out := make([]keyPreview, 0, len(merged))
	for _, k := range sortedAnyKeys(merged) {
		out = append(out, keyPreview{Key: k, Value: previewHelmValue(merged[k])})
	}
	return out, nil
}

// previewHelmValue masks a scalar value and collapses nested structures to a
// shape hint so no secret material leaks into the key preview.
func previewHelmValue(v any) string {
	switch t := v.(type) {
	case map[string]any:
		return "{…}"
	case []any:
		return "[…]"
	case string:
		return maskValue(t)
	default:
		return maskValue(fmt.Sprintf("%v", t))
	}
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

func sortedAnyKeys(m map[string]any) []string {
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
