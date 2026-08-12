package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
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
		params := props["params"].(schema.Schema)
		Expect(params["x-clicky-component"]).To(Equal("es-params"))
		param := params["items"].(schema.Schema)
		Expect(param["x-clicky-component"]).To(Equal("es-param"))
		Expect(param["properties"].(schema.Schema)["field"].(schema.Schema)["x-clicky-component"]).To(Equal("es-param-field"))
		roles := param["properties"].(schema.Schema)["role"].(schema.Schema)["enum"]
		Expect(roles).To(ContainElements("limit", "offset", "time-from", "time-to"))
		Expect(roles).ToNot(ContainElement("cursor"))
		column := props["columns"].(schema.Schema)["items"].(schema.Schema)
		columnProps := column["properties"].(schema.Schema)
		Expect(columnProps["kind"].(schema.Schema)["enum"]).To(ContainElement("timestamp"))
		typeSchema := columnProps["type"].(schema.Schema)
		Expect(typeSchema["enum"]).To(ContainElements("key_value", "key_values", "json"))
		Expect(typeSchema["x-enum-labels"].(map[string]string)).To(HaveKeyWithValue("key_values", "[]KeyValue"))
	})

	It("picks profile references from the same hierarchy the sidebar shows", func() {
		props := schema.Profile()["properties"].(schema.Schema)

		// The dotted naming convention IS the import graph (jms.incoming imports
		// jms), so imports is a hierarchical picker rather than free text.
		imports := props["imports"].(schema.Schema)
		Expect(imports["type"]).To(Equal("array"))
		Expect(imports["items"].(schema.Schema)["type"]).To(Equal("string"),
			"the stored value stays a plain string array")
		importLookup := imports["x-clicky-lookup"].(schema.Schema)
		Expect(importLookup["url"]).To(Equal("/api/v1/profiles"))
		// Must match profileFilter.Key() in cmd/query/profiles; a mismatch yields
		// an empty option set with no error.
		Expect(importLookup["filter"]).To(Equal("profile"))
		Expect(importLookup["multi"]).To(BeTrue())
		// "." and "/" split; "-" deliberately does not, or remote-debugger would
		// shatter into a hierarchy that does not exist.
		Expect(importLookup["hierarchy"].(schema.Schema)["delimiters"]).To(Equal("./"))

		dest := props["reconcile"].(schema.Schema)["properties"].(schema.Schema)["dest"].(schema.Schema)
		Expect(dest["type"]).To(Equal("string"))
		destLookup := dest["x-clicky-lookup"].(schema.Schema)
		Expect(destLookup["filter"]).To(Equal("profile"))
		Expect(destLookup["multi"]).To(BeFalse())
		Expect(destLookup["hierarchy"].(schema.Schema)["delimiters"]).To(Equal("./"))
	})

	It("lets a profile override the glyph its provider type would give it", func() {
		icon := schema.Profile()["properties"].(schema.Schema)["icon"].(schema.Schema)
		Expect(icon["type"]).To(Equal("string"))
		Expect(icon["description"]).To(ContainSubstring("provider"))
	})

	It("presents params as summary rows identified by their own properties", func() {
		props := schema.Profile()["properties"].(schema.Schema)
		params := props["params"].(schema.Schema)
		Expect(params["x-array-display"]).To(Equal("accordion"))
		// The array's description doubles as the add row's zero-item copy.
		Expect(params["description"]).To(ContainSubstring("{{.params.<name>}}"))

		item := params["x-item"].(schema.Schema)
		Expect(item["title"]).To(Equal([]string{"label", "name"}))
		Expect(item["glyph"]).To(Equal("type"))
		Expect(item["badge"]).To(Equal("role"))
		Expect(item["flag"]).To(Equal("required"))
		Expect(item["noun"]).To(Equal("parameter"))

		param := params["items"].(schema.Schema)
		Expect(param["title"]).To(Equal("Parameter"))
		Expect(param["x-columns"]).To(Equal("auto"))

		paramProps := param["properties"].(schema.Schema)
		typeProp := paramProps["type"].(schema.Schema)
		Expect(typeProp["x-enum-icons"].(map[string]string)).To(HaveKeyWithValue("list", "list-dashes"))
		Expect(typeProp["x-enum-tones"].(map[string]string)).To(HaveKeyWithValue("list", "indigo"))
		// Without this, x-enum-icons alone flips the control to the icon-card
		// grid, which is far too wide for an accordion body.
		Expect(typeProp["x-enum-display"]).To(Equal("combobox"))
		Expect(paramProps["role"].(schema.Schema)["x-enum-display"]).To(Equal("combobox"))

		// The two long fields take the whole row rather than one narrow column.
		Expect(paramProps["options"].(schema.Schema)["x-col-span"]).To(Equal("full"))
		Expect(paramProps["description"].(schema.Schema)["x-col-span"]).To(Equal("full"))
	})

	It("uses a nested provider discriminator with icon combobox options", func() {
		s := schema.Profile()
		props := s["properties"].(schema.Schema)
		Expect(props["namespace"].(schema.Schema)["x-clicky-component"]).To(Equal("k8s-namespace-selector"))
		provider := props["provider"].(schema.Schema)
		Expect(provider["x-discriminator"]).To(Equal("type"))
		typeProp := provider["properties"].(schema.Schema)["type"].(schema.Schema)
		Expect(typeProp["x-enum-display"]).To(Equal("combobox"))
		Expect(typeProp["x-enum-icons"].(map[string]string)).To(HaveLen(17))
	})

	It("exposes the reconcile block a profile stores its habitual join in", func() {
		props := schema.Profile()["properties"].(schema.Schema)
		reconcile := props["reconcile"].(schema.Schema)["properties"].(schema.Schema)

		Expect(reconcile["dest"].(schema.Schema)["type"]).To(Equal("string"))
		Expect(reconcile["timeColumn"]).To(HaveKey("description"))

		// A range narrows both sides to the same keys, which the per-side row
		// cap it replaced could not: two sides cut at N rows each are two
		// different key sets, so the bound itself produced one-sided findings.
		keyRange := reconcile["range"].(schema.Schema)
		Expect(keyRange["properties"].(schema.Schema)).To(HaveKey("from"))
		Expect(keyRange["properties"].(schema.Schema)).To(HaveKey("to"))
		Expect(reconcile).ToNot(HaveKey("limit"))

		// Columns and CEL are alternatives the engine rejects together, so both
		// are offered and the description is what says to pick one.
		key := reconcile["key"].(schema.Schema)
		Expect(key["properties"].(schema.Schema)).To(HaveKey("columns"))
		Expect(key["properties"].(schema.Schema)).To(HaveKey("cel"))
		Expect(key["description"]).To(ContainSubstring("never both"))
	})

	It("bundles every provider component and enriches inline URLs", func() {
		Expect(schema.ProfileComponents()).To(HaveLen(17))
		source := schema.ProfileSource()
		provider := source["properties"].(schema.Schema)["provider"].(schema.Schema)
		firstSourceBranch := provider["allOf"].([]any)[0].(schema.Schema)
		Expect(firstSourceBranch["then"].(schema.Schema)["$ref"]).To(HavePrefix("profiles/"))

		bundled := schema.Profile()
		Expect(bundled["$defs"].(schema.Schema)).To(HaveLen(17))
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

var _ = Describe("Search specification schema", func() {
	searchProp := func(providerType string) schema.Schema {
		options := schema.ProfileComponents()[providerType]["properties"].(schema.Schema)["options"].(schema.Schema)
		return options["properties"].(schema.Schema)["search"].(schema.Schema)
	}

	DescribeTable("delegates the search specification to the query builder",
		func(providerType string) {
			search := searchProp(providerType)
			Expect(search["x-clicky-component"]).To(Equal("es-query-builder"))
			Expect(search["properties"].(schema.Schema)).To(HaveKey("query"))
		},
		Entry("opensearch", "opensearch"),
		Entry("opentelemetry", "opentelemetry"),
	)

	It("no longer offers the ad-hoc opentelemetry params object", func() {
		options := schema.ProfileComponents()["opentelemetry"]["properties"].(schema.Schema)["options"].(schema.Schema)
		Expect(options["properties"].(schema.Schema)).ToNot(HaveKey("params"))
	})

	// The builder reads its operator vocabulary off the schema, so a new operator
	// that does not reach x-es-operators would be invisible in the UI.
	It("carries the whole operator catalog, keyed exactly as esdsl emits it", func() {
		operators := searchProp("opensearch")["x-es-operators"].([]any)
		Expect(operators).To(HaveLen(len(esdsl.Catalog())))

		emitted := map[string]map[string]any{}
		for _, entry := range operators {
			info := entry.(map[string]any)
			emitted[info["op"].(string)] = info
		}
		for _, info := range esdsl.Catalog() {
			Expect(emitted).To(HaveKey(string(info.Op)))
			Expect(emitted[string(info.Op)]).To(HaveKeyWithValue("label", info.Label))
			Expect(emitted[string(info.Op)]).To(HaveKeyWithValue("arity", string(info.Arity)))
		}
		Expect(emitted["terms"]).To(HaveKeyWithValue("needsField", true))
		Expect(emitted["bool"]).To(HaveKeyWithValue("group", true))
		Expect(emitted["match"]).To(HaveKeyWithValue("analyzed", true))
		Expect(emitted["match"]["fieldTypes"]).To(ConsistOf("text", "keyword"))
	})

	It("carries the occur list and the qualifier-to-operator table", func() {
		search := searchProp("opensearch")
		Expect(search["x-es-occurs"]).To(Equal([]string{"filter", "must", "should", "must_not"}))

		qualifiers := search["x-es-qualifiers"].(map[string][]string)
		Expect(qualifiers).To(HaveLen(len(esdsl.QualifierNames())))
		Expect(qualifiers["scoreMode"]).To(Equal([]string{"nested"}))
		Expect(qualifiers["caseInsensitive"]).To(ConsistOf("term", "terms", "prefix", "wildcard", "regexp"))
	})

	It("offers every supported numeric time field format", func() {
		properties := searchProp("opensearch")["properties"].(schema.Schema)
		format := properties["timeFieldFormat"].(schema.Schema)
		Expect(format["enum"]).To(Equal(esdsl.TimeFieldFormats()))
	})

	// Every enum the form offers must be one the compiler accepts, or the form
	// would hand the author a value that fails on save.
	It("offers only qualifier values the compiler accepts", func() {
		condition := searchProp("opensearch")["properties"].(schema.Schema)["query"].(schema.Schema)
		props := condition["properties"].(schema.Schema)
		Expect(props["op"].(schema.Schema)["enum"]).To(HaveLen(len(esdsl.Catalog())))
		Expect(props["matchOperator"].(schema.Schema)["enum"]).To(Equal(esdsl.MatchOperators()))
		Expect(props["multiMatchType"].(schema.Schema)["enum"]).To(Equal(esdsl.MultiMatchTypes()))
		Expect(props["scoreMode"].(schema.Schema)["enum"]).To(Equal(esdsl.ScoreModes()))
		Expect(props["sort"]).To(BeNil())

		for _, value := range esdsl.MultiMatchTypes() {
			Expect(esdsl.Condition{
				Op: esdsl.OpMultiMatch, Fields: []string{"message"},
				Value: esdsl.Literal("boom"), MultiMatchType: value,
			}.Validate("query")).To(Succeed())
		}
	})

	// A schema-driven form materialises declared defaults onto the object it is
	// editing. A qualifier only some operators accept would then be injected onto
	// operators that reject it, and the condition would fail to compile the moment
	// a saved profile is reopened.
	It("declares no default on a qualifier only some operators accept", func() {
		props := searchProp("opensearch")["properties"].(schema.Schema)["query"].(schema.Schema)["properties"].(schema.Schema)
		for name := range esdsl.Qualifiers() {
			Expect(props[name].(schema.Schema)).ToNot(HaveKey("default"),
				"qualifier %q is operator-specific, so a default would be injected onto operators that reject it", name)
		}
	})

	// The tree is expanded rather than $ref'd, so the innermost level must stop.
	It("expands the condition tree to a bounded depth", func() {
		condition := searchProp("opensearch")["properties"].(schema.Schema)["query"].(schema.Schema)
		depth := 0
		for {
			depth++
			children, nested := condition["properties"].(schema.Schema)["conditions"]
			if !nested {
				break
			}
			condition = children.(schema.Schema)["items"].(schema.Schema)
		}
		Expect(depth).To(Equal(3))
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
			{Name: "id", Type: query.ColumnTypeNumber, Kind: query.ColumnKindTimestamp, Format: "float", Unit: "short"},
			{Name: "secret", Hidden: true},
		},
	}
	s, profileInstanceErr := schema.ProfileInstance(p)

	It("resolves the profile's column filters", func() {
		Expect(profileInstanceErr).ToNot(HaveOccurred())
	})

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
		Expect(cols[0].(schema.Schema)["format"]).To(Equal("float"))
		Expect(cols[0].(schema.Schema)["unit"]).To(Equal("short"))
	})

	It("omits x-clicky-render when the profile has no render mode", func() {
		Expect(s).ToNot(HaveKey("x-clicky-render"))
	})

	It("emits the render mode and no per-column sort/filter flags for a logs profile", func() {
		logsSchema, err := schema.ProfileInstance(query.Profile{
			Name:   "jaeger spans",
			Render: query.RenderLogs,
			Columns: []query.ColumnDef{
				{Name: "message", CEL: "row.operationName"},
				{Name: "duration", Type: query.ColumnTypeDuration},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(logsSchema["x-clicky-render"]).To(Equal("logs"))
		for _, c := range logsSchema["x-clicky-columns"].([]any) {
			col := c.(schema.Schema)
			Expect(col).ToNot(HaveKey("sortable"))
			Expect(col).ToNot(HaveKey("filterable"))
		}
	})
})

var _ = Describe("Profile column editor schema", func() {
	It("defines the requested order, strict enums, and Type versus Role help", func() {
		profile := schema.ProfileSource()
		columns := profile["properties"].(schema.Schema)["columns"].(schema.Schema)
		items := columns["items"].(schema.Schema)
		props := items["properties"].(schema.Schema)

		Expect(props["label"].(schema.Schema)["x-clicky-order"]).To(Equal(0))
		Expect(props["name"].(schema.Schema)["x-clicky-order"]).To(Equal(1))
		Expect(props["source"].(schema.Schema)["x-clicky-order"]).To(Equal(2))
		Expect(props["type"].(schema.Schema)["x-clicky-order"]).To(Equal(3))
		jsonPath := props["jsonpath"].(schema.Schema)
		Expect(jsonPath["x-clicky-order"]).To(Equal(9))
		Expect(jsonPath["x-clicky-component"]).To(Equal("jsonpath-picker"))
		Expect(props["cel"].(schema.Schema)["x-clicky-order"]).To(Equal(8))
		Expect(props["hidden"].(schema.Schema)["x-clicky-order"]).To(Equal(11))
		filter := props["filter"].(schema.Schema)
		Expect(filter["x-clicky-order"]).To(Equal(10))
		filterProps := filter["properties"].(schema.Schema)
		// The field is optional now: a column whose own definition names a
		// backend field does not restate it, and an enumerated filter names none.
		Expect(filter).ToNot(HaveKey("required"))
		Expect(filterProps["field"].(schema.Schema)["description"]).To(ContainSubstring("required only when the column implies none"))
		Expect(filterProps["kind"].(schema.Schema)["enum"]).To(Equal(query.ColumnFilterKindValues()))
		Expect(props["kind"].(schema.Schema)["title"]).To(Equal("Role"))
		Expect(props["kind"].(schema.Schema)["description"]).To(ContainSubstring("independent of Type"))
		Expect(props["format"].(schema.Schema)["enum"]).To(Equal([]string{"date", "float", "duration", "bytes", "currency"}))
		Expect(props["unit"].(schema.Schema)["enum"]).To(Equal([]string{
			"none", "short", "percent", "percentunit", "bytes", "decbytes", "Bps", "binBps", "ms", "s",
		}))
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
