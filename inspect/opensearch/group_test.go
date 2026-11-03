package opensearchinspect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	opensearch "github.com/opensearch-project/opensearch-go/v2"
)

func TestIndexGroups(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenSearch index groups")
}

var _ = Describe("index groups", func() {
	It("keeps the wildcard identity when discovered members change", func() {
		targets := []Target{{Name: "logs-2026-11-01", Kind: "index"}, {Name: "logs-2026-11-02", Kind: "index"}}
		first := RollupTargets(targets)[0]
		targets = append(targets, Target{Name: "logs-2026-11-03", Kind: "index"})
		second := RollupTargets(targets)[0]
		Expect([]string{first.Name, first.QueryName(), second.Name, second.QueryName()}).To(Equal([]string{"logs-*", "logs-*", "logs-*", "logs-*"}))
		Expect(second.Members).To(Equal([]string{"logs-2026-11-01", "logs-2026-11-02", "logs-2026-11-03"}))
	})

	It("uses the wildcard for field caps and mapping even when the catalog is truncated", func() {
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			switch {
			case strings.Contains(r.URL.Path, "_resolve"):
				_, _ = fmt.Fprint(w, `{"indices":[{"name":"logs-2026-11-01"},{"name":"logs-2026-11-02"}]}`)
			case strings.HasSuffix(r.URL.Path, "_field_caps"):
				_, _ = fmt.Fprint(w, `{"fields":{"timestamp":{"date":{"searchable":true,"aggregatable":true}}}}`)
			default:
				_, _ = fmt.Fprint(w, `{"logs-2026-11-03":{"mappings":{"timestamp":{"full_name":"timestamp","mapping":{"timestamp":{"format":"epoch_millis"}}}}}}`)
			}
		}))
		defer server.Close()
		client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
		Expect(err).NotTo(HaveOccurred())
		inspector, err := New(client, Options{MaxTargets: 1})
		Expect(err).NotTo(HaveOccurred())
		catalog, err := inspector.Targets(context.Background(), TargetRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Truncated).To(BeTrue())
		Expect(catalog.Targets).To(HaveLen(1))
		fields, err := inspector.Fields(context.Background(), FieldRequest{Target: catalog.Targets[0], Names: []string{"timestamp"}, IncludeFormats: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(fields.Fields).To(HaveLen(1))
		Expect(fields.Fields[0].Format).To(Equal("epoch_millis"))
		Expect(paths[1:]).To(Equal([]string{"/logs-*/_field_caps", "/logs-*/_mapping/field/timestamp"}))
	})
})
