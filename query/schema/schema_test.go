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
			then := b["then"].(map[string]any)
			if ref, ok := then["$ref"].(string); ok {
				const prefix = "#/$defs/"
				return s["$defs"].(schema.Schema)[ref[len(prefix):]].(map[string]any)
			}
			return then
		}
	}
	return nil
}

func authBranchFor(authentication schema.Schema, authType string) schema.Schema {
	for _, raw := range authentication["allOf"].([]any) {
		branch := raw.(schema.Schema)
		condition := branch["if"].(schema.Schema)["properties"].(schema.Schema)
		if condition["authType"].(schema.Schema)["const"] == authType {
			return branch["then"].(schema.Schema)
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
		Expect(enum).To(ContainElement(models.ConnectionTypeOpenTelemetry))
		Expect(enum).To(HaveLen(56))
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

	It("marks type as the discriminator with an icon combobox for every type", func() {
		Expect(s["x-discriminator"]).To(Equal("type"))
		typeProp := s["properties"].(schema.Schema)["type"].(schema.Schema)
		Expect(typeProp["x-enum-display"]).To(Equal("combobox"))
		icons := typeProp["x-enum-icons"].(map[string]string)
		Expect(icons).To(HaveLen(56))
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

	It("gives HTTP connections a segmented conditional authentication form", func() {
		then := branchFor(s, models.ConnectionTypeHTTP)
		props := then["properties"].(schema.Schema)
		for _, key := range []string{"url", "insecure_tls", "properties"} {
			Expect(props).To(HaveKey(key))
		}
		Expect(props).ToNot(HaveKey("username"))
		Expect(props).ToNot(HaveKey("password"))
		Expect(props).ToNot(HaveKey("certificate"))

		authentication := props["properties"].(schema.Schema)
		selector := authentication["properties"].(schema.Schema)["authType"].(schema.Schema)
		Expect(selector["enum"]).To(Equal([]string{"none", "basic", "oauth", "mtls"}))
		Expect(selector["default"]).To(Equal("none"))
		Expect(selector["x-enum-display"]).To(Equal("segmented"))

		basic := authBranchFor(authentication, "basic")
		Expect(basic["required"]).To(ConsistOf("username", "password"))
		Expect(basic["properties"].(schema.Schema)["password"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-secret-selector"))

		oauth := authBranchFor(authentication, "oauth")
		Expect(oauth["required"]).To(ConsistOf("clientID", "clientSecret", "tokenURL"))
		Expect(oauth["properties"].(schema.Schema)).To(HaveKey("scopes"))

		mtls := authBranchFor(authentication, "mtls")
		Expect(mtls["required"]).To(ConsistOf("cert", "key"))
		Expect(mtls["properties"].(schema.Schema)).To(HaveKey("ca"))
	})

	It("extends the HTTP form for OpenSearch", func() {
		then := branchFor(s, models.ConnectionTypeOpenSearch)
		props := then["properties"].(schema.Schema)
		Expect(props).To(HaveKey("url"))
		Expect(props).To(HaveKey("insecure_tls"))
		Expect(props["properties"].(schema.Schema)["properties"].(schema.Schema)).To(HaveKey("authType"))
	})

	It("scopes OpenTelemetry to a required nested OpenSearch connection", func() {
		then := branchFor(s, models.ConnectionTypeOpenTelemetry)
		properties := then["properties"].(schema.Schema)["properties"].(schema.Schema)
		Expect(properties["required"]).To(ContainElement("connection"))
		connection := properties["properties"].(schema.Schema)["connection"].(schema.Schema)
		lookup := connection["x-clicky-lookup"].(schema.Schema)
		scope := lookup["scope"].(schema.Schema)
		Expect(scope["map"].(map[string][]string)[models.ConnectionTypeOpenTelemetry]).To(Equal([]string{models.ConnectionTypeOpenSearch}))
	})

	It("surfaces certificate per type: optional for kubernetes, required for GCP", func() {
		k8s := branchFor(s, models.ConnectionTypeKubernetes)
		Expect(k8s["properties"].(schema.Schema)).To(HaveKey("certificate"))
		// Kubernetes cert is optional: only the universal name/type fields are required.
		Expect(k8s["required"]).To(ConsistOf("name", "type"))

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

	It("emits external source refs and a local-ref bundle for all 56 components", func() {
		Expect(schema.ConnectionComponents()).To(HaveLen(56))
		source := schema.ConnectionSource()
		firstSourceBranch := source["allOf"].([]any)[0].(schema.Schema)
		Expect(firstSourceBranch["then"].(schema.Schema)["$ref"]).To(HavePrefix("connections/"))

		bundled := schema.Connection()
		Expect(bundled["$defs"].(schema.Schema)).To(HaveLen(56))
		firstBundledBranch := bundled["allOf"].([]any)[0].(schema.Schema)
		Expect(firstBundledBranch["then"].(schema.Schema)["$ref"]).To(HavePrefix("#/$defs/"))
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
		props := s["properties"].(schema.Schema)
		param := props["params"].(schema.Schema)["items"].(schema.Schema)
		Expect(param["properties"].(schema.Schema)["role"].(schema.Schema)["enum"]).To(ContainElements("limit", "offset", "time-from", "time-to"))
		column := props["columns"].(schema.Schema)["items"].(schema.Schema)
		columnProps := column["properties"].(schema.Schema)
		Expect(columnProps["kind"].(schema.Schema)["enum"]).To(ContainElement("timestamp"))
		typeSchema := columnProps["type"].(schema.Schema)
		Expect(typeSchema["enum"]).To(ContainElements("key_value", "key_values", "json"))
		Expect(typeSchema["x-enum-labels"].(map[string]string)).To(HaveKeyWithValue("key_values", "[]KeyValue"))
	})

	It("uses a nested provider discriminator with icon combobox options", func() {
		s := schema.Profile()
		props := s["properties"].(schema.Schema)
		Expect(props["namespace"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-namespace-selector"))
		provider := props["provider"].(schema.Schema)
		Expect(provider["x-discriminator"]).To(Equal("type"))
		typeProp := provider["properties"].(schema.Schema)["type"].(schema.Schema)
		Expect(typeProp["x-enum-display"]).To(Equal("combobox"))
		Expect(typeProp["x-enum-icons"].(map[string]string)).To(HaveLen(12))
	})

	It("bundles every provider component and enriches inline URLs", func() {
		Expect(schema.ProfileComponents()).To(HaveLen(12))
		source := schema.ProfileSource()
		provider := source["properties"].(schema.Schema)["provider"].(schema.Schema)
		firstSourceBranch := provider["allOf"].([]any)[0].(schema.Schema)
		Expect(firstSourceBranch["then"].(schema.Schema)["$ref"]).To(HavePrefix("profiles/"))

		bundled := schema.Profile()
		Expect(bundled["$defs"].(schema.Schema)).To(HaveLen(12))
		http := bundled["$defs"].(schema.Schema)["http"].(schema.Schema)
		options := http["properties"].(schema.Schema)["options"].(schema.Schema)
		url := options["properties"].(schema.Schema)["url"].(schema.Schema)
		Expect(url["x-clicky-component"]).To(Equal("k8s-url-selector"))
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
			{Name: "id", Type: query.ColumnTypeString, Kind: query.ColumnKindTimestamp},
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
		Expect(cols[0].(schema.Schema)["kind"]).To(Equal("timestamp"))
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

var _ = Describe("Schema bundling", func() {
	It("rejects unresolved and cyclic external refs", func() {
		_, err := schema.Bundle(schema.Schema{"$ref": "missing.json"}, nil)
		Expect(err).To(MatchError(ContainSubstring("unresolved schema ref")))

		_, err = schema.Bundle(
			schema.Schema{"$ref": "self.json"},
			map[string]schema.Schema{"self.json": {"$ref": "self.json"}},
		)
		Expect(err).To(MatchError(ContainSubstring("cyclic schema ref")))
	})
})
