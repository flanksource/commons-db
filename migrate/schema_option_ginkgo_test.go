package migrate

import (
	"net/url"
	"strings"
	"testing/fstest"

	"ariga.io/atlas/sql/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema-scoped migrations", func() {
	DescribeTable("validates normalized schema identifiers",
		func(name string, valid bool) {
			err := ValidateSchemaName(name)
			if valid {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		},
		Entry("public", "public", true),
		Entry("normalized context", "agent_namespace_context", true),
		Entry("leading underscore", "_private", true),
		Entry("63 bytes", "s"+strings.Repeat("a", 62), true),
		Entry("empty", "", false),
		Entry("uppercase", "AGENT", false),
		Entry("hyphen", "agent-context", false),
		Entry("quoted", `"agent"`, false),
		Entry("qualified", "public.agent", false),
		Entry("64 bytes", "s"+strings.Repeat("a", 63), false),
	)

	It("defaults options to public and retains a selected schema", func() {
		Expect(resolveOptions(nil).schema).To(Equal(defaultSchema))
		cfg := resolveOptions([]Option{WithSchema("agent_namespace_context")})
		Expect(cfg.schema).To(Equal("agent_namespace_context"))
	})

	It("preserves default-schema keyword connections", func(ctx SpecContext) {
		const connection = "host=localhost dbname=example"
		got, err := prepareSchemaConnection(ctx, schemaConnectionOptions{Connection: connection, Schema: defaultSchema})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(connection))
	})

	It("rejects an invalid selected schema before connecting", func(ctx SpecContext) {
		filesystem := fstest.MapFS{"schema.hcl": &fstest.MapFile{Data: []byte(`schema "public" {}`)}}
		err := Apply(ctx, "postgres://unused", filesystem, WithSchema(""))
		Expect(err).To(MatchError(ContainSubstring("migration schema: schema name is empty")))
	})

	It("creates a schema-scoped URL without losing existing parameters", func() {
		got, err := ConnectionForSchema("postgres://user:pass@localhost/database?sslmode=disable&application_name=migrate", "agent_namespace_context")
		Expect(err).NotTo(HaveOccurred())
		parsed, err := url.Parse(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Query().Get("search_path")).To(Equal("agent_namespace_context"))
		Expect(parsed.Query().Get("sslmode")).To(Equal("disable"))
		Expect(parsed.Query().Get("application_name")).To(Equal("migrate"))
	})

	It("rejects a non-URL connection when it cannot scope it safely", func() {
		_, err := ConnectionForSchema("host=localhost dbname=example", "agent_namespace_context")
		Expect(err).To(MatchError(ContainSubstring("URL-form")))
	})

	It("includes non-public schemas in metadata scope without changing public scope", func() {
		Expect(migrationScope(defaultSchema, "runtime")).To(Equal("runtime"))
		Expect(migrationScope("agent_namespace_context", "runtime")).To(Equal("agent_namespace_context:runtime"))
	})

	It("remaps the desired public schema and its table references", func() {
		public := schema.New("public")
		table := schema.NewTable("threads").SetSchema(public)
		public.AddTables(table)
		realm := schema.NewRealm(public)

		Expect(remapDesiredSchema(realm, "agent_namespace_context")).To(Succeed())
		Expect(realm.Schemas).To(HaveLen(1))
		Expect(realm.Schemas[0].Name).To(Equal("agent_namespace_context"))
		Expect(table.Schema).To(BeIdenticalTo(realm.Schemas[0]))
	})

	It("remaps security targets owned by the default schema", func() {
		spec := securitySpec{Permissions: []permissionSpec{
			{Target: "schema:public"},
			{Target: "table:public.threads"},
			{Target: "column:public.threads.id"},
			{Target: "sequence:public.thread_seq"},
		}}

		got := remapSecuritySchema(spec, "agent_namespace_context")
		Expect(got.Permissions).To(Equal([]permissionSpec{
			{Target: "schema:agent_namespace_context"},
			{Target: "table:agent_namespace_context.threads"},
			{Target: "column:agent_namespace_context.threads.id"},
			{Target: "sequence:agent_namespace_context.thread_seq"},
		}))
		Expect(spec.Permissions[0].Target).To(Equal("schema:public"))
	})
})
