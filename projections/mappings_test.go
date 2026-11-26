package projections

import (
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Projection mappings", func() {
	ginkgo.It("renders dynamic projection values as text", func() {
		program, err := compileProjectionExpression(`text(source.health) + ":" + text(source.alerts) + ":" + text(source.disabled)`)
		Expect(err).ToNot(HaveOccurred())

		value, err := evalProjectionValue(program, map[string]any{
			"source": map[string]any{"health": "healthy", "alerts": float64(3), "disabled": true},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(Equal("healthy:3:true"))
	})
	ginkgo.It("preserves a CEL null as a native null", func() {
		program, err := compileProjectionExpression(`null`)
		Expect(err).ToNot(HaveOccurred())

		value, err := evalProjectionValue(program, map[string]any{})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(BeNil())
	})
	ginkgo.It("applies JSONPath mappings and list strategies to one matched target", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", `schema_version: 1
entries:
  - external_id: group-1
    evidence: {}
    roles: [reader]
    grants:
      - context: beta
        role: old
      - context: prod
        role: owner
`)
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "groups"},
			manifest:   filepath.Join(dir, "projection.yaml"),
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.external_id"},
				Set: map[string]ProjectionSet{
					"$.evidence.config": {Value: "source.config"},
					"$.roles":           {Value: "source.roles", Strategy: "mergeUnique"},
					"$.grants":          {Value: "[source.grant]", Strategy: "replaceMatching", Match: "item.context == context.name"},
					"$.tenant":          {Value: "source.tenant", When: `source.tenant != ""`},
				},
			},
		}
		source := projectionSourceResult{
			Context: map[string]any{"name": "beta"},
			Items: []map[string]any{{
				"id":     "group-1",
				"config": map[string]any{"displayName": "Platform"},
				"roles":  []any{"reader", "owner"},
				"grant":  map[string]any{"context": "beta", "role": "reader"},
				"tenant": "",
			}},
		}

		result, err := applyProjection(projection, source, false)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Changed).To(ConsistOf("group-1 $.evidence.config", "group-1 $.roles", "group-1 $.grants"))
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		var document map[string]any
		Expect(yaml.Unmarshal(body, &document)).To(Succeed())
		entries := document["entries"].([]any)
		entry := entries[0].(map[string]any)
		Expect(entry["roles"]).To(Equal([]any{"reader", "owner"}))
		Expect(entry).NotTo(HaveKey("tenant"))
		Expect(entry["evidence"]).To(Equal(map[string]any{"config": map[string]any{"displayName": "Platform"}}))
		Expect(entry["grants"]).To(Equal([]any{
			map[string]any{"context": "beta", "role": "reader"},
			map[string]any{"context": "prod", "role": "owner"},
		}))
	})
	ginkgo.It("preserves flow-style target entries when replacing a list", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", "entries:\n  - {id: repo, evidence: [old], owner: kept}\n")
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "flow"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set:    map[string]ProjectionSet{"$.evidence": {Value: "source.evidence"}},
			},
		}

		_, err := applyProjection(projection, projectionSourceResult{
			Items: []map[string]any{{"id": "repo", "evidence": []any{"new"}}},
		}, false)

		Expect(err).ToNot(HaveOccurred())
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(Equal("entries:\n  - {id: repo, evidence: [new], owner: kept}\n"))
	})
	ginkgo.It("creates unmatched targets by default", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "create"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set: map[string]ProjectionSet{
					"$.id":      {Value: "source.id"},
					"$.aliases": {Value: "source.aliases", Strategy: "mergeUnique"},
					"$.name":    {Value: "source.name"},
				},
			},
		}

		result, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{{
			"id": "principal-1", "name": "Principal", "aliases": []any{"principal@example.com"},
		}}}, false)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Created).To(Equal([]string{"principal-1"}))
		Expect(result.Missing).To(BeEmpty())
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		var document map[string]any
		Expect(yaml.Unmarshal(body, &document)).To(Succeed())
		Expect(document["entries"]).To(Equal([]any{map[string]any{
			"aliases": []any{"principal@example.com"}, "id": "principal-1", "name": "Principal",
		}}))
	})
	ginkgo.It("reports unmatched targets when creation is explicitly disabled", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		create := false
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "enrich"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]", Create: &create},
				Match:  []string{"source.id == target.id"},
				Set:    map[string]ProjectionSet{"$.name": {Value: "source.name"}},
			},
		}

		result, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{{
			"id": "principal-1", "name": "Principal",
		}}}, false)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Created).To(BeEmpty())
		Expect(result.Missing).To(Equal([]string{"principal-1"}))
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(Equal("entries: []\n"))
	})
	ginkgo.It("keeps block-style list indentation when appending to a matched target", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", `entries:
  - id: repo
    properties:
      - existing property
`)
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "indent"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set: map[string]ProjectionSet{
					"$.properties": {Value: `["added property"]`, Strategy: "mergeUnique"},
				},
			},
		}

		_, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{{"id": "repo"}}}, false)

		Expect(err).ToNot(HaveOccurred())
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(Equal(`entries:
  - id: repo
    properties:
      - existing property
      - added property
`))
	})
	ginkgo.It("keeps indentation when a flow-style list inside a block-style entry grows", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", `entries:
  - id: repo
    properties: [not_evidenced]
    owner: kept
`)
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "mixed-style"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set: map[string]ProjectionSet{
					"$.properties": {Value: `["one", "two"]`, Strategy: "mergeUnique"},
				},
			},
		}

		_, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{{"id": "repo"}}}, false)

		Expect(err).ToNot(HaveOccurred())
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			Expect(len(line)-len(strings.TrimLeft(line, " "))).To(BeNumerically("<=", 6),
				"line is indented far past its parent key: %q", line)
		}
	})
	ginkgo.It("preserves leading comments such as the schema language-server link", func() {
		dir := ginkgo.GinkgoT().TempDir()
		header := "# yaml-language-server: $schema=./target.schema.json\n"
		targetPath := writeProjectionTestFile(dir, "target.yaml", header+`schema_version: 1
entries:
  - id: repo
    name: old
`)
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "comments"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set:    map[string]ProjectionSet{"$.name": {Value: "source.name"}},
			},
		}

		_, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{{
			"id": "repo", "name": "new",
		}}}, false)

		Expect(err).ToNot(HaveOccurred())
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(HavePrefix(header))
		Expect(string(body)).To(ContainSubstring("name: new"))
	})
	ginkgo.It("renders timestamps as the calendar date registers record", func() {
		program, err := compileProjectionExpression(`date(source.first_observed)`)
		Expect(err).ToNot(HaveOccurred())

		value, err := evalProjectionValue(program, map[string]any{
			"source": map[string]any{"first_observed": "2026-02-06T13:30:38Z"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(Equal("2026-02-06"))
	})
	ginkgo.It("capitalises vendor-lowercased names so registers read as proper nouns", func() {
		program, err := compileProjectionExpression(`source.distro.title() + " " + source.version`)
		Expect(err).ToNot(HaveOccurred())

		value, err := evalProjectionValue(program, map[string]any{
			"source": map[string]any{"distro": "ubuntu", "version": "24.04"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(Equal("Ubuntu 24.04"))
	})
	ginkgo.It("parses a catalog property string into a number", func() {
		program, err := compileProjectionExpression(
			`{"high": source.properties["High Alerts"].int(), "score": source.properties["OpenSSF Score"].float()}`)
		Expect(err).ToNot(HaveOccurred())

		value, err := evalProjectionValue(program, map[string]any{
			"source": map[string]any{"properties": map[string]any{"High Alerts": "17", "OpenSSF Score": "6.9"}},
		})

		Expect(err).ToNot(HaveOccurred())
		// A count writes as 17, not 17.0 — a constructed map is converted through
		// structpb, whose only numeric type is a float64, so integral values are
		// narrowed back on the way out.
		Expect(value).To(Equal(map[string]any{"high": int64(17), "score": 6.9}))
	})
	ginkgo.It("refuses a property that is not a number rather than reading it as zero", func() {
		program, err := compileProjectionExpression(`source.alerts.int()`)
		Expect(err).ToNot(HaveOccurred())

		_, err = evalProjectionValue(program, map[string]any{"source": map[string]any{"alerts": "several"}})

		Expect(err).To(MatchError(ContainSubstring(`cannot parse "several"`)))
	})
	ginkgo.It("carries a number too large for an int as a double", func() {
		program, err := compileProjectionExpression(`source.bytes.float()`)
		Expect(err).ToNot(HaveOccurred())

		const beyondInt64 = "18446744073709551616" // 2^64
		value, err := evalProjectionValue(program, map[string]any{
			"source": map[string]any{"bytes": beyondInt64},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(BeNumerically(">", 0))
		Expect(value).To(Equal(math.Pow(2, 64)))
	})
	ginkgo.It("refuses NaN rather than writing it to a register", func() {
		program, err := compileProjectionExpression(`source.score.float()`)
		Expect(err).ToNot(HaveOccurred())

		_, err = evalProjectionValue(program, map[string]any{"source": map[string]any{"score": "NaN"}})

		Expect(err).To(MatchError(ContainSubstring("NaN")))
	})
	ginkgo.It("rejects a date() argument that is not a timestamp", func() {
		program, err := compileProjectionExpression(`date(source.first_observed)`)
		Expect(err).ToNot(HaveOccurred())

		_, err = evalProjectionValue(program, map[string]any{
			"source": map[string]any{"first_observed": "last tuesday"},
		})

		Expect(err).To(MatchError(ContainSubstring("RFC3339")))
	})
	ginkgo.It("drops source items rejected by spec.source.where and counts them", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", "entries: []\n")
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "overdue"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{
					Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}},
					Where: `timestamp(source.first_observed) < timestamp(context.observed_at) - ` +
						`duration(source.severity == "critical" ? "168h" : "720h")`,
				},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set:    map[string]ProjectionSet{"$.id": {Value: "source.id"}},
			},
		}
		source := projectionSourceResult{
			Context: map[string]any{"observed_at": "2026-08-10T00:00:00Z"},
			Items: []map[string]any{
				{"id": "critical-overdue", "severity": "critical", "first_observed": "2026-07-22T00:00:00Z"},
				{"id": "critical-inside", "severity": "critical", "first_observed": "2026-08-08T00:00:00Z"},
				{"id": "high-overdue", "severity": "high", "first_observed": "2023-05-01T00:00:00Z"},
				{"id": "high-inside", "severity": "high", "first_observed": "2026-08-01T00:00:00Z"},
			},
		}

		result, err := applyProjection(projection, source, false)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Filtered).To(Equal(2))
		Expect(result.Created).To(Equal([]string{"critical-overdue", "high-overdue"}))
	})
	ginkgo.It("folds many sources into one target when aggregation is enabled", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", `entries:
  - repository: flanksource/mission-control
    integrity_properties: [hand-authored property]
`)
		create := false
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "scorecard"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]", Create: &create, Aggregate: true},
				Match:  []string{"target.repository == source.config.name"},
				Set: map[string]ProjectionSet{
					"$.integrity_properties": {
						Value:    `["OpenSSF " + source.analyzer + " score " + text(source.analysis.score)]`,
						Strategy: "replaceMatching",
						Match:    `item.startsWith("OpenSSF " + source.analyzer + " ")`,
					},
				},
			},
		}
		repo := map[string]any{"name": "flanksource/mission-control"}
		source := projectionSourceResult{Items: []map[string]any{
			{"id": "a", "analyzer": "Signed-Releases", "analysis": map[string]any{"score": float64(0)}, "config": repo},
			{"id": "b", "analyzer": "Token-Permissions", "analysis": map[string]any{"score": float64(9)}, "config": repo},
			{"id": "c", "analyzer": "Pinned-Dependencies", "analysis": map[string]any{"score": float64(5)}, "config": repo},
		}}

		result, err := applyProjection(projection, source, false)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Matched).To(Equal(1))
		Expect(result.Aggregated).To(Equal(2))
		body, err := os.ReadFile(targetPath)
		Expect(err).ToNot(HaveOccurred())
		var document map[string]any
		Expect(yaml.Unmarshal(body, &document)).To(Succeed())
		entry := document["entries"].([]any)[0].(map[string]any)
		Expect(entry["integrity_properties"]).To(Equal([]any{
			"hand-authored property",
			"OpenSSF Signed-Releases score 0",
			"OpenSSF Token-Permissions score 9",
			"OpenSSF Pinned-Dependencies score 5",
		}))
	})
	ginkgo.It("still rejects two sources claiming one target when aggregation is off", func() {
		dir := ginkgo.GinkgoT().TempDir()
		targetPath := writeProjectionTestFile(dir, "target.yaml", "entries:\n  - repository: repo-1\n    checks: []\n")
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "collision"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: targetPath, Select: "$.entries[*]"},
				Match:  []string{"target.repository == source.repository"},
				Set:    map[string]ProjectionSet{"$.checks": {Value: "[source.id]", Strategy: "mergeUnique"}},
			},
		}

		_, err := applyProjection(projection, projectionSourceResult{Items: []map[string]any{
			{"id": "first", "repository": "repo-1"},
			{"id": "second", "repository": "repo-1"},
		}}, false)

		Expect(err).To(MatchError(ContainSubstring("is matched by both first and second")))
	})
	ginkgo.It("rejects scalar mappings under aggregation because fan-in keeps only the last source", func() {
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "scalar-under-aggregate"},
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: "target.yaml", Select: "$.entries[*]", Aggregate: true},
				Match:  []string{"target.repository == source.repository"},
				Set:    map[string]ProjectionSet{"$.deployment_approval": {Value: "source.reason"}},
			},
		}

		Expect(projection.validate()).To(MatchError(ContainSubstring("must use strategy mergeUnique or replaceMatching")))
	})
	ginkgo.It("validates the complete target document against its schema", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeProjectionTestFile(dir, "target.yaml", "schema_version: 1\nentries: []\n")
		writeProjectionTestFile(dir, "target.schema.json", `{
  "type": "object",
  "required": ["schema_version", "entries"],
  "properties": {
    "schema_version": {"const": 1},
    "entries": {"type": "array"}
  }
}`)
		projection := Projection{
			APIVersion: projectionAPIVersion,
			Kind:       projectionKind,
			Metadata:   ProjectionMetadata{Name: "schema"},
			manifest:   filepath.Join(dir, "projection.yaml"),
			Spec: ProjectionSpec{
				Source: ProjectionSource{Query: ProjectionQuery{Exec: &ProjectionExecQuery{Script: "printf '[]'"}}},
				Target: &ProjectionTarget{Path: "target.yaml", Schema: "target.schema.json", Select: "$.entries[*]"},
				Match:  []string{"source.id == target.id"},
				Set:    map[string]ProjectionSet{"$.name": {Value: "source.name"}},
			},
		}

		Expect(verifyProjection(projection)).To(Succeed())
	})
})
