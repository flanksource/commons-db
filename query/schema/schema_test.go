package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSchema(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schema Suite")
}

// branchFor returns the then-clause for the if/then branch whose discriminator
// matches typ, or nil when no branch exists.
func branchFor(s schema.Schema, typ string) map[string]any {
	for _, raw := range s["allOf"].([]any) {
		b := raw.(map[string]any)
		ifClause := b["if"].(map[string]any)["properties"].(map[string]any)
		if ifClause["type"].(map[string]any)["const"] == typ {
			return b["then"].(map[string]any)
		}
	}
	return nil
}

var _ = Describe("Connection schema", func() {
	s := schema.Connection()

	It("is a valid Draft 2020-12 object that marshals to JSON", func() {
		Expect(s["$schema"]).To(Equal(schema.Draft))
		Expect(s["type"]).To(Equal("object"))
		_, err := json.Marshal(s)
		Expect(err).ToNot(HaveOccurred())
	})

	It("enumerates every connection type", func() {
		enum := s["properties"].(schema.Schema)["type"].(schema.Schema)["enum"].([]string)
		Expect(enum).To(ContainElements(
			models.ConnectionTypePostgres, models.ConnectionTypeAWS,
			models.ConnectionTypeKubernetes, models.ConnectionTypeZulipChat,
		))
		// guards against drift from the models.ConnectionType* constant set
		Expect(enum).To(HaveLen(54))
	})

	It("requires the DSN url for the postgres branch", func() {
		then := branchFor(s, models.ConnectionTypePostgres)
		Expect(then).ToNot(BeNil())
		Expect(then["required"]).To(ContainElement("url"))
		Expect(then["properties"].(schema.Schema)).To(HaveKey("url"))
	})

	It("nests AWS region/profile under the properties object", func() {
		then := branchFor(s, models.ConnectionTypeAWS)
		props := then["properties"].(schema.Schema)["properties"].(schema.Schema)["properties"].(schema.Schema)
		Expect(props).To(HaveKey("region"))
		Expect(props).To(HaveKey("profile"))
	})
})

var _ = Describe("Profile schema", func() {
	It("requires profile and provider", func() {
		s := schema.Profile()
		Expect(s["required"]).To(ConsistOf("profile", "provider"))
		Expect(s["properties"].(schema.Schema)).To(HaveKey("params"))
	})
})

var _ = Describe("ProfileInstance schema", func() {
	p := query.Profile{
		Name: "activities",
		Params: []query.ParamDef{
			{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}, Required: true},
			{Name: "limit", Type: query.ParamTypeNumber, Default: 50},
		},
		Columns: []query.ColumnDef{
			{Name: "id", Type: query.ColumnTypeString},
			{Name: "secret", Hidden: true},
		},
	}
	s := schema.ProfileInstance(p)

	It("exposes params as form properties with required + enum", func() {
		props := s["properties"].(schema.Schema)
		Expect(props).To(HaveKey("region"))
		Expect(props).To(HaveKey("limit"))
		Expect(props["region"].(schema.Schema)["enum"]).To(Equal([]string{"US", "EU"}))
		Expect(s["required"]).To(ContainElement("region"))
	})

	It("lists visible columns in x-clicky-columns and drops hidden ones", func() {
		cols := s["x-clicky-columns"].([]any)
		Expect(cols).To(HaveLen(1))
		Expect(cols[0].(schema.Schema)["name"]).To(Equal("id"))
	})
})
