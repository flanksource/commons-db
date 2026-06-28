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
		Expect(enum).To(HaveLen(55))
	})

	It("keeps the base form to name/namespace/type/properties", func() {
		props := s["properties"].(schema.Schema)
		Expect(props).To(HaveKey("name"))
		Expect(props).To(HaveKey("namespace"))
		Expect(props).To(HaveKey("type"))
		Expect(props).To(HaveKey("properties"))
		// the per-type fields live on the branches, not the base form
		Expect(props).ToNot(HaveKey("url"))
		Expect(props).ToNot(HaveKey("username"))
		Expect(props).ToNot(HaveKey("password"))
		Expect(props).ToNot(HaveKey("certificate"))
		Expect(props["namespace"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-namespace-selector"))
	})

	It("marks type as the discriminator with an icon grid for every type", func() {
		Expect(s["x-discriminator"]).To(Equal("type"))
		typeProp := s["properties"].(schema.Schema)["type"].(schema.Schema)
		Expect(typeProp["x-enum-display"]).To(Equal("grid"))
		icons := typeProp["x-enum-icons"].(map[string]string)
		Expect(icons).To(HaveLen(55))
		Expect(icons[models.ConnectionTypePostgres]).To(Equal("postgres"))
	})

	It("orders the base fields via per-property x-clicky-order", func() {
		props := s["properties"].(schema.Schema)
		Expect(props["name"].(schema.Schema)["x-clicky-order"]).To(BeNumerically("==", 0))
		Expect(props["namespace"].(schema.Schema)["x-clicky-order"]).To(BeNumerically("==", 1))
		Expect(props["properties"].(schema.Schema)["x-clicky-order"]).To(BeNumerically("==", 7))
	})

	It("gives the postgres (SQL) branch url+credentials but no certificate", func() {
		then := branchFor(s, models.ConnectionTypePostgres)
		Expect(then).ToNot(BeNil())
		Expect(then["required"]).To(ContainElement("url"))
		props := then["properties"].(schema.Schema)
		Expect(props).To(HaveKey("url"))
		Expect(props).To(HaveKey("username"))
		Expect(props).To(HaveKey("password"))
		Expect(props).ToNot(HaveKey("certificate"))
		Expect(props).ToNot(HaveKey("insecure_tls"))
		Expect(props["url"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-url-selector"))
		Expect(props["url"].(schema.Schema)["x-clicky-order"]).To(BeNumerically("==", 2))
	})

	It("gives the HTTP branch the rich form with certificate + nested oauth", func() {
		then := branchFor(s, models.ConnectionTypeHTTP)
		props := then["properties"].(schema.Schema)
		for _, key := range []string{"url", "insecure_tls", "username", "password", "certificate"} {
			Expect(props).To(HaveKey(key))
		}
		Expect(props["password"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-secret-selector"))
		Expect(props["password"].(schema.Schema)["x-clicky-default-source"]).To(Equal("secret"))
		// bearer/oauth credentials nest under the connection properties map
		oauth := props["properties"].(schema.Schema)["properties"].(schema.Schema)
		Expect(oauth).To(HaveKey("bearer"))
		Expect(oauth).To(HaveKey("clientID"))
		Expect(oauth["bearer"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-secret-selector"))
	})

	It("extends the HTTP form for OpenSearch", func() {
		then := branchFor(s, models.ConnectionTypeOpenSearch)
		props := then["properties"].(schema.Schema)
		Expect(props).To(HaveKey("url"))
		Expect(props).To(HaveKey("certificate"))
		Expect(props).To(HaveKey("insecure_tls"))
	})

	It("surfaces certificate per type: optional for kubernetes, required for GCP", func() {
		k8s := branchFor(s, models.ConnectionTypeKubernetes)
		Expect(k8s["properties"].(schema.Schema)).To(HaveKey("certificate"))
		// kubernetes cert is optional: the branch declares no required fields
		Expect(k8s).ToNot(HaveKey("required"))

		gcp := branchFor(s, models.ConnectionTypeGCP)
		Expect(gcp["properties"].(schema.Schema)).To(HaveKey("certificate"))
		Expect(gcp["required"]).To(ContainElement("certificate"))
	})

	It("only tailors branches for known connection types", func() {
		for typ := range schema.TailoredProviderTypes() {
			Expect(allConnectionTypesSet()).To(HaveKey(typ), "tailored type %q not in the connection enum", typ)
			Expect(branchFor(s, typ)).ToNot(BeNil(), "missing branch for tailored type %q", typ)
		}
	})

	It("maps every connection type to an icon", func() {
		icons := s["properties"].(schema.Schema)["type"].(schema.Schema)["x-enum-icons"].(map[string]string)
		for typ := range allConnectionTypesSet() {
			Expect(icons).To(HaveKey(typ), "missing icon for connection type %q", typ)
		}
	})
})

// allConnectionTypesSet is the connection type enum as a set, for the drift guard.
func allConnectionTypesSet() map[string]struct{} {
	enum := schema.Connection()["properties"].(schema.Schema)["type"].(schema.Schema)["enum"].([]string)
	set := map[string]struct{}{}
	for _, t := range enum {
		set[t] = struct{}{}
	}
	return set
}

var _ = Describe("Profile schema", func() {
	It("requires profile and provider", func() {
		s := schema.Profile()
		Expect(s["required"]).To(ConsistOf("profile", "provider"))
		Expect(s["properties"].(schema.Schema)).To(HaveKey("params"))
	})

	It("makes provider.connection an x-clicky-lookup picker scoped by provider type", func() {
		s := schema.Profile()
		provider := s["properties"].(schema.Schema)["provider"].(schema.Schema)
		conn := provider["properties"].(schema.Schema)["connection"].(schema.Schema)

		lookup := conn["x-clicky-lookup"].(schema.Schema)
		Expect(lookup["url"]).To(Equal("/api/v1/connection"))
		Expect(lookup["filter"]).To(Equal("connection"))

		scope := lookup["scope"].(schema.Schema)
		Expect(scope["param"]).To(Equal("types"))
		Expect(scope["from"]).To(Equal("provider.type"))

		// sqlserver maps to the "sql_server" connection type (the value the
		// connection list filters on — guards the underscore mismatch), and the
		// generic sql provider offers every SQL backend.
		typeMap := scope["map"].(map[string][]string)
		Expect(typeMap["sqlserver"]).To(Equal([]string{"sql_server"}))
		Expect(typeMap["sql"]).To(ConsistOf("postgres", "mysql", "sql_server", "clickhouse"))
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

	It("omits x-clicky-render when the profile has no render mode", func() {
		Expect(s).ToNot(HaveKey("x-clicky-render"))
	})

	It("emits the render mode and no per-column sort/filter flags for a logs profile", func() {
		logsSchema := schema.ProfileInstance(query.Profile{
			Name:   "jaeger spans",
			Render: query.RenderLogs,
			Columns: []query.ColumnDef{
				{Name: "message", CEL: "row.operationName"},
				{Name: "duration", Type: query.ColumnTypeDuration},
			},
		})
		Expect(logsSchema["x-clicky-render"]).To(Equal("logs"))
		for _, c := range logsSchema["x-clicky-columns"].([]any) {
			col := c.(schema.Schema)
			Expect(col).ToNot(HaveKey("sortable"))
			Expect(col).ToNot(HaveKey("filterable"))
		}
	})
})
