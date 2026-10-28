package projections

import (
	"os"

	"github.com/flanksource/commons-db/context"
	"github.com/goccy/go-yaml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Nested projection targets", func() {
	It("resolves every wildcard to a concrete write path without changing neighboring disks", func() {
		dir := GinkgoT().TempDir()
		path := writeProjectionTestFile(dir, "target.yaml", "servers:\n  - name: alpha\n    disks: [{name: data, capacity: 100}]\n  - name: beta\n    disks: [{name: logs, capacity: 200}, {name: selected, capacity: 300}]\n")
		p := execProjection(`printf '%s' '{"name":"selected","p50":0}'`)
		create := false
		p.Spec.Target = &ProjectionTarget{Path: path, Select: "$.servers[*].disks[*]", Create: &create}
		p.Spec.Match = []string{"source.name == target.name"}
		p.Spec.Set = map[string]ProjectionSet{"$.p50": {Value: "source.p50"}}
		results, err := Apply(context.New(), []Projection{p}, Options{})
		Expect(err).NotTo(HaveOccurred(), "%+v", results)
		body, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var result map[string]any
		Expect(yaml.Unmarshal(body, &result)).To(Succeed())
		Expect(result).To(Equal(map[string]any{"servers": []any{
			map[string]any{"name": "alpha", "disks": []any{map[string]any{"name": "data", "capacity": uint64(100)}}},
			map[string]any{"name": "beta", "disks": []any{map[string]any{"name": "logs", "capacity": uint64(200)}, map[string]any{"name": "selected", "capacity": uint64(300), "p50": float64(0)}}},
		}}))
	})
	It("adds nested measurements without replacing their existing parent or sibling measurements", func() {
		path := writeProjectionTestFile(GinkgoT().TempDir(), "target.yaml", "servers:\n  - name: sql\n    host: {name: instance, scope: instance, disks: [{name: data}]}\n")
		p := execProjection(`printf '%s' '{"name":"sql"}'`)
		p.Spec.Target = &ProjectionTarget{Path: path, Select: "$.servers[*]"}
		p.Spec.Match = []string{"source.name == target.name"}
		p.Spec.Set = map[string]ProjectionSet{"$.host.cpu.used.avg": {Value: "5"}, "$.host.memory.available.min": {Value: "100"}}
		results, err := Apply(context.New(), []Projection{p}, Options{})
		Expect(err).NotTo(HaveOccurred(), "%+v", results)
		body, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring("name: instance"))
		Expect(string(body)).To(ContainSubstring("cpu:"))
		Expect(string(body)).To(ContainSubstring("disks:"))
		Expect(string(body)).To(ContainSubstring("available:"))
	})
	It("creates nested entries only when there is one unambiguous parent sequence", func() {
		path := writeProjectionTestFile(GinkgoT().TempDir(), "target.yaml", "servers:\n  - name: sql\n    databases: [{name: insurance, queries: []}]\n")
		p := execProjection(`printf '%s' '{"name":"query-a"}'`)
		p.Spec.Target = &ProjectionTarget{Path: path, Select: "$.servers[*].databases[*].queries[*]"}
		p.Spec.Match = []string{"source.name == target.name"}
		p.Spec.Set = map[string]ProjectionSet{"$.name": {Value: "source.name"}}
		results, err := Apply(context.New(), []Projection{p}, Options{})
		Expect(err).NotTo(HaveOccurred(), "%+v", results)
		body, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var result map[string]any
		Expect(yaml.Unmarshal(body, &result)).To(Succeed())
		Expect(result).To(Equal(map[string]any{"servers": []any{map[string]any{
			"name": "sql", "databases": []any{map[string]any{"name": "insurance", "queries": []any{map[string]any{"name": "query-a"}}}},
		}}}))
	})
	It("rejects creation under multiple parent sequences without changing the target", func() {
		before := "servers: [{name: alpha, queries: []}, {name: beta, queries: []}]\n"
		path := writeProjectionTestFile(GinkgoT().TempDir(), "target.yaml", before)
		p := execProjection(`printf '%s' '{"name":"query-a"}'`)
		p.Spec.Target = &ProjectionTarget{Path: path, Select: "$.servers[*].queries[*]"}
		p.Spec.Match = []string{"source.name == target.name"}
		p.Spec.Set = map[string]ProjectionSet{"$.name": {Value: "source.name"}}
		results, err := Apply(context.New(), []Projection{p}, Options{})
		Expect(err).To(HaveOccurred())
		Expect(results[0].Error).To(ContainSubstring("exactly one target sequence"))
		body, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal(before))
	})
})
