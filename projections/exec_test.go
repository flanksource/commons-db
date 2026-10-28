package projections

import (
	gocontext "context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/context"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func writeProjectionTestFile(dir, name, body string) string {
	path := filepath.Join(dir, name)
	Expect(os.WriteFile(path, []byte(body), 0600)).To(Succeed())
	return path
}

func execProjection(script string) Projection {
	return Projection{APIVersion: projectionAPIVersion, Kind: projectionKind,
		Metadata: ProjectionMetadata{Name: "example"}, Spec: ProjectionSpec{
			Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: script}}},
		}}
}

var _ = ginkgo.Describe("Exec projection source", func() {
	ginkgo.It("selects rows from a command's JSON envelope", func() {
		p := execProjection(`printf '%s' '{"entries":[{"name":"alpha"},{"name":"beta"}]}'`)
		p.Spec.Source.Query.Exec.Select = "$.entries"
		rows, err := queryExecProjection(context.New(), p, map[string]any{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"name": "alpha"}, {"name": "beta"}}))
	})

	for _, output := range []string{"null", "[1]", `{"name":"alpha"} {}`, "invalid"} {
		ginkgo.It("rejects malformed row output "+output, func() {
			_, err := queryExecProjection(context.New(), execProjection("printf '%s' '"+output+"'"), nil)
			Expect(err).To(HaveOccurred())
		})
	}

	ginkgo.It("quotes template arguments without interpreting shell syntax", func() {
		p := execProjection(`printf '{"name":"%s"}' {{shellQuote .context.name}}`)
		value := "acme' $(exit 9)"
		rows, err := queryExecProjection(context.New(), p, map[string]any{"name": value})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"name": value}}))
	})

	ginkgo.It("runs eligible target entries sequentially and wraps each payload", func() {
		dir := ginkgo.GinkgoT().TempDir()
		p := execProjection(`printf '{"count":%s}' {{shellQuote .target.count}}`)
		p.Spec.Source.Query.Exec.Mode = "perTarget"
		p.Spec.Target = &ProjectionTarget{Path: writeProjectionTestFile(dir, "target.yaml", "entries:\n  - {name: alpha, url: 'https://example.com/alpha', count: '2', active: true}\n  - {name: ignored, active: false}\n  - {name: beta, url: 'https://example.com/beta', count: '3', active: true}\n"), Select: "$.entries[*]", Filter: "target.active"}
		rows, err := queryExecProjection(context.New(), p, map[string]any{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{
			{"name": "alpha", "url": "https://example.com/alpha", "count": "2", "active": true, "body": map[string]any{"count": float64(2)}},
			{"name": "beta", "url": "https://example.com/beta", "count": "3", "active": true, "body": map[string]any{"count": float64(3)}},
		}))
	})

	ginkgo.It("fails on a command's nonzero exit", func() {
		_, err := queryExecProjection(context.New(), execProjection("exit 7"), nil)
		Expect(err).To(MatchError(ContainSubstring("7")))
	})

	ginkgo.It("cancels a timed out process and cleans invocation files", func() {
		p := execProjection("sleep 10")
		dir := ginkgo.GinkgoT().TempDir()
		p.manifest = filepath.Join(dir, "projection.yaml")
		p.Spec.Source.Query.Exec.Timeout = 100 * time.Millisecond
		started := time.Now()
		_, err := queryExecProjection(context.New(), p, nil)
		Expect(err).To(MatchError(gocontext.DeadlineExceeded))
		Expect(time.Since(started)).To(BeNumerically("<", 3*time.Second))
		entries, err := os.ReadDir(filepath.Join(dir, ".tmp"))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})
})
