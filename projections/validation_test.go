package projections

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Projection validation", func() {
	for _, test := range []struct {
		name    string
		query   ProjectionQuery
		message string
	}{
		{"missing source", ProjectionQuery{}, "exactly one"},
		{"multiple sources", ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}, HTTP: &ProjectionHTTPQuery{HTTPConnection: connection.HTTPConnection{URL: "https://example.com"}}}, "exactly one"},
		{"empty script", ProjectionQuery{Exec: &ProjectionExecQuery{}}, "script is required"},
		{"invalid mode", ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'", Mode: "invalid"}}, "mode must be"},
		{"negative timeout", ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'", Timeout: -time.Second}}, "timeout must not be negative"},
		{"invalid selection", ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'", Select: "$["}}, "source select"},
		{"per target without target", ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'", Mode: "perTarget"}}, "require spec.target"},
		{"http without target", ProjectionQuery{HTTP: &ProjectionHTTPQuery{HTTPConnection: connection.HTTPConnection{URL: "https://example.com"}}}, "require spec.target"},
	} {
		ginkgo.It("rejects "+test.name, func() {
			p := execProjection("printf '[]'")
			p.Spec.Source.Query = test.query
			Expect(p.validate()).To(MatchError(ContainSubstring(test.message)))
		})
	}

	ginkgo.It("rejects unknown manifest fields and unsupported scraper connections", func() {
		dir := ginkgo.GinkgoT().TempDir()
		manifest := "apiVersion: projections.flanksource.com/v1alpha1\nkind: Projection\nmetadata: {name: example}\nspec:\n  source:\n    query:\n      exec:\n        script: printf '[]'\n"
		path := writeProjectionTestFile(dir, "projection.yaml", manifest+"        unknown: true\n")
		_, err := Load(LoadOptions{Paths: []string{path}})
		Expect(err).To(MatchError(ContainSubstring("unknown")))
		path = writeProjectionTestFile(dir, "projection.yaml", manifest+"        connections:\n          fromConfigItem: inventory\n")
		_, err = Load(LoadOptions{Paths: []string{path}})
		Expect(err).To(MatchError(ContainSubstring("fromConfigItem is not supported")))
	})

	ginkgo.It("verifies every shipped example offline", func() {
		docs, err := Load(LoadOptions{Paths: []string{"examples"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = Verify(docs)
		Expect(err).NotTo(HaveOccurred())
	})

	ginkgo.It("fails all per-target execution before mapping when a later invocation fails", func() {
		dir := ginkgo.GinkgoT().TempDir()
		original := "entries: [{id: alpha}, {id: beta}]\n"
		target := writeProjectionTestFile(dir, "target.yaml", original)
		p := mappedProjection(dir, `{{if eq .target.id "beta"}}exit 7{{else}}printf '%s' '{"name":"updated"}'{{end}}`)
		p.Spec.Source.Query.Exec.Mode = "perTarget"
		p.Spec.Set = map[string]ProjectionSet{"$.name": {Value: "source.body.name"}}
		_, err := Apply(context.New(), []Projection{p}, Options{})
		Expect(err).To(HaveOccurred())
		body, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal(original))
		Expect(filepath.Join(dir, ".tmp")).To(BeADirectory())
	})
})
