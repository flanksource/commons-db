package projections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func mappedProjection(dir, script string) Projection {
	p := execProjection(script)
	p.manifest = filepath.Join(dir, "projection.yaml")
	p.Spec.Target = &ProjectionTarget{Path: "target.yaml", Select: "$.entries[*]"}
	p.Spec.Match = []string{"source.id == target.id"}
	p.Spec.Set = map[string]ProjectionSet{"$.id": {Value: "source.id"}, "$.name": {Value: "source.name"}}
	return p
}

func connectionDir(dir string) string {
	ginkgo.GinkgoT().Setenv("XDG_CONFIG_HOME", dir)
	connections := filepath.Join(dir, "flanksource", "connections")
	Expect(os.MkdirAll(connections, 0700)).To(Succeed())
	return connections
}

var _ = ginkgo.Describe("Projection operations", func() {
	ginkgo.It("verifies without commands or connection lookup, then dry runs without writing", func() {
		dir := ginkgo.GinkgoT().TempDir()
		target := writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		p := mappedProjection(dir, `printf '%s' '{"id":"api","name":"API"}'`)
		p.Spec.Source.Query.Exec.Connections.AWS = &connection.AWSConnection{ConnectionName: "missing"}
		_, err := Verify([]Projection{p})
		Expect(err).NotTo(HaveOccurred())
		p.Spec.Source.Query.Exec.Connections.AWS = nil
		results, err := Apply(context.New(), []Projection{p}, Options{DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(results[0].Created).To(Equal([]string{"api"}))
		Expect(os.ReadFile(target)).To(Equal([]byte("entries: []\n")))
		_, err = Apply(context.New(), []Projection{p}, Options{})
		Expect(err).NotTo(HaveOccurred())
		body, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring("name: API"))
	})

	ginkgo.It("continues after source failure and retains all per-projection statuses", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		broken := mappedProjection(dir, "exit 8")
		good := mappedProjection(dir, `printf '%s' '{"id":"api","name":"API"}'`)
		broken.Metadata.Name, good.Metadata.Name = "broken", "good"
		results, err := Apply(context.New(), []Projection{broken, good}, Options{})
		Expect(err).To(MatchError("1 of 2 projections failed"))
		Expect(results[0].Status).To(Equal(projectionFailed))
		Expect(results[0].Error).To(ContainSubstring("8"))
		Expect(results[1].Status).To(Equal(projectionApplied))
		Expect(results[1].Created).To(Equal([]string{"api"}))
	})

	ginkgo.It("preflights every expression before any source executes", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		first := mappedProjection(dir, "touch should-not-exist")
		second := first
		second.Metadata.Name = "invalid"
		second.Spec.Source.Where = "source."
		_, err := Apply(context.New(), []Projection{first, second}, Options{})
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(dir, "should-not-exist")).NotTo(BeAnExistingFile())
	})

	ginkgo.It("resolves AWS from only the global store and cleans credential files", func() {
		dir := ginkgo.GinkgoT().TempDir()
		connections := connectionDir(dir)
		writeProjectionTestFile(connections, "aws.yaml", `apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata: {name: inventory, namespace: examples}
spec:
  aws:
    region: eu-west-1
    accessKey: {value: example-access}
    secretKey: {value: example-secret}
`)
		p := execProjection(`test -f "$AWS_SHARED_CREDENTIALS_FILE"
test "$AWS_DEFAULT_REGION" = eu-west-1
rg --quiet 'aws_access_key_id = example-access' "$AWS_SHARED_CREDENTIALS_FILE"
printf '%s' '{"configured":true}'`)
		p.manifest = filepath.Join(dir, "projection.yaml")
		p.Spec.Source.Query.Exec.Connections.AWS = &connection.AWSConnection{ConnectionName: "connection://examples/inventory"}
		results, err := Run(context.New(), []Projection{p}, Options{Context: map[string]any{"name": "local"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(results[0].Items).To(Equal([]map[string]any{{"configured": true}}))
		Expect(results[0].Context).To(HaveKeyWithValue("name", "local"))
		entries, err := os.ReadDir(filepath.Join(dir, ".tmp"))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
		p.Spec.Source.Query.Exec.Connections.AWS.ConnectionName = "missing"
		_, err = Run(context.New(), []Projection{p}, Options{})
		Expect(err).To(MatchError(ContainSubstring("not found in")))
	})

	ginkgo.It("injects OpenSearch settings and redacts credentials from command failures", func() {
		dir := ginkgo.GinkgoT().TempDir()
		p := execProjection(`printf '%s' '{"ready":true}'`)
		p.manifest = filepath.Join(dir, "projection.yaml")
		p.Spec.Source.Query.Exec.Connections.OpenSearch = &connection.OpenSearchConnection{URLs: []string{"https://search.example.com"}, Index: "inventory", HTTPBasicAuth: types.HTTPBasicAuth{Authentication: types.Authentication{Username: types.EnvVar{ValueStatic: "example-user"}, Password: types.EnvVar{ValueStatic: "example-password"}}}}
		p.Spec.Source.Query.Exec.Script = `test -n "$OPENSEARCH_URLS"
test -n "$OPENSEARCH_INDEX"
printf '%s' "$OPENSEARCH_PASSWORD" >&2
exit 3`
		_, err := Run(context.New(), []Projection{p}, Options{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("example-password"))
		Expect(err.Error()).To(ContainSubstring("3"))
	})
})

var _ = ginkgo.Describe("HTTP projection source", func() {
	ginkgo.It("hydrates a named local HTTP connection and preserves order across 202 responses", func() {
		var requests []string
		attempts := map[string]int{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer example-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			requests = append(requests, r.URL.Path)
			attempts[r.URL.Path]++
			if attempts[r.URL.Path] == 1 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			Expect(json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"state": "ready"}})).To(Succeed())
		}))
		defer server.Close()
		dir := ginkgo.GinkgoT().TempDir()
		connections := connectionDir(dir)
		writeProjectionTestFile(connections, "http.yaml", `apiVersion: mission-control.flanksource.com/v1
kind: Connection
metadata: {name: inventory}
spec:
  http:
    url: `+server.URL+`
    bearer: {value: example-token}
`)
		writeProjectionTestFile(dir, "target.yaml", "entries:\n  - {id: alpha, active: true}\n  - {id: skip, active: false}\n  - {id: beta, active: true}\n")
		p := mappedProjection(dir, "")
		p.Spec.Source.Query = ProjectionQuery{HTTP: &ProjectionHTTPQuery{HTTPConnection: connection.HTTPConnection{URL: server.URL + "/{{.id}}", ConnectionName: "inventory"}, Select: "$.data", Accepted: ProjectionHTTPAccepted{Attempts: 2, Wait: time.Millisecond}}}
		p.Spec.Target.Filter = "target.active"
		_, err := Verify([]Projection{p})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(BeEmpty())
		results, err := Run(context.New(), []Projection{p}, Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(Equal([]string{"/alpha", "/alpha", "/beta", "/beta"}))
		Expect(results[0].Items).To(Equal([]map[string]any{
			{"id": "alpha", "active": true, "url": server.URL + "/alpha", "body": map[string]any{"state": "ready"}},
			{"id": "beta", "active": true, "url": server.URL + "/beta", "body": map[string]any{"state": "ready"}},
		}))
	})

	for _, response := range []string{"unavailable", "accepted", "malformed", "missing"} {
		ginkgo.It("reports warnings without rows for "+response, func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch response {
				case "unavailable":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "accepted":
					w.WriteHeader(http.StatusAccepted)
				case "malformed":
					_, _ = w.Write([]byte("invalid"))
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()
			dir := ginkgo.GinkgoT().TempDir()
			writeProjectionTestFile(dir, "target.yaml", "entries: [{id: alpha}]\n")
			p := mappedProjection(dir, "")
			p.Spec.Source.Query = ProjectionQuery{HTTP: &ProjectionHTTPQuery{HTTPConnection: connection.HTTPConnection{URL: server.URL + "/{{.id}}"}, Select: "$.missing", Accepted: ProjectionHTTPAccepted{Attempts: 1}}}
			results, err := Run(context.New(), []Projection{p}, Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(results[0].Items).To(BeEmpty())
			Expect(results[0].Warnings).To(HaveLen(1))
		})
	}
})

var _ = ginkgo.Describe("Projection loading", func() {
	ginkgo.It("loads multi-document files recursively and rejects duplicate names", func() {
		dir := ginkgo.GinkgoT().TempDir()
		manifest := `apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata: {name: alpha}
spec:
  source:
    query:
      exec:
        script: printf '[]'
        timeout: 1m
`
		writeProjectionTestFile(dir, "a.yaml", manifest+"---\n"+strings.ReplaceAll(manifest, "name: alpha", "name: beta"))
		writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		loaded, err := Load(LoadOptions{Paths: []string{dir}})
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded).To(HaveLen(2))
		Expect(loaded[0].Metadata.Name).To(Equal("alpha"))
		Expect(loaded[1].Metadata.Name).To(Equal("beta"))
		Expect(loaded[0].Spec.Source.Query.Exec.Timeout).To(Equal(time.Minute))
		writeProjectionTestFile(dir, "duplicate.yaml", manifest)
		_, err = Load(LoadOptions{Paths: []string{dir}})
		Expect(err).To(MatchError(ContainSubstring("duplicate projection metadata.name")))
	})
})
