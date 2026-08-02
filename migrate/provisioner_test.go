package migrate

import (
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zclconf/go-cty/cty"
)

var _ = Describe("migration provisioner fingerprint", func() {
	baseFS := func() fstest.MapFS {
		return fstest.MapFS{
			"migrations/schema.hcl": &fstest.MapFile{Data: []byte(`schema "public" {}`)},
			"migrations/seed.sql":   &fstest.MapFile{Data: []byte("SELECT 1")},
			"migrations/readme.txt": &fstest.MapFile{Data: []byte("ignored")},
		}
	}

	It("is deterministic across filesystem and variable map order", func(ctx SpecContext) {
		first := NewProvisioner(baseFS(),
			WithDir("migrations"),
			WithName("query"),
			WithExclude("audit.*", "internal.*"),
			WithVariables(map[string]cty.Value{
				"enabled": cty.True,
				"name":    cty.StringVal("example"),
			}),
		)
		secondFS := fstest.MapFS{
			"migrations/readme.txt": &fstest.MapFile{Data: []byte("changed but ignored")},
			"migrations/seed.sql":   &fstest.MapFile{Data: []byte("SELECT 1")},
			"migrations/schema.hcl": &fstest.MapFile{Data: []byte(`schema "public" {}`)},
		}
		second := NewProvisioner(secondFS,
			WithVariables(map[string]cty.Value{
				"name":    cty.StringVal("example"),
				"enabled": cty.True,
			}),
			WithExclude("internal.*", "audit.*"),
			WithName("query"),
			WithDir("migrations"),
		)

		firstFingerprint, err := first.Fingerprint(ctx)
		Expect(err).NotTo(HaveOccurred())
		secondFingerprint, err := second.Fingerprint(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstFingerprint).To(Equal(secondFingerprint))
	})

	DescribeTable("changes when migration behavior changes",
		func(ctx SpecContext, mutate func(fstest.MapFS) *SchemaProvisioner) {
			baseline, err := NewProvisioner(baseFS(), WithDir("migrations"), WithName("query")).Fingerprint(ctx)
			Expect(err).NotTo(HaveOccurred())
			changed, err := mutate(baseFS()).Fingerprint(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).NotTo(Equal(baseline))
		},
		Entry("HCL content", func(files fstest.MapFS) *SchemaProvisioner {
			files["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte("schema \"other\" {}")}
			return NewProvisioner(files, WithDir("migrations"), WithName("query"))
		}),
		Entry("SQL content", func(files fstest.MapFS) *SchemaProvisioner {
			files["migrations/seed.sql"] = &fstest.MapFile{Data: []byte("SELECT 2")}
			return NewProvisioner(files, WithDir("migrations"), WithName("query"))
		}),
		Entry("migration path", func(files fstest.MapFS) *SchemaProvisioner {
			files["migrations/renamed.sql"] = files["migrations/seed.sql"]
			delete(files, "migrations/seed.sql")
			return NewProvisioner(files, WithDir("migrations"), WithName("query"))
		}),
		Entry("migration directory", func(files fstest.MapFS) *SchemaProvisioner {
			files["schema.hcl"] = files["migrations/schema.hcl"]
			files["seed.sql"] = files["migrations/seed.sql"]
			delete(files, "migrations/schema.hcl")
			delete(files, "migrations/seed.sql")
			return NewProvisioner(files, WithName("query"))
		}),
		Entry("scope", func(files fstest.MapFS) *SchemaProvisioner {
			return NewProvisioner(files, WithDir("migrations"), WithName("other"))
		}),
		Entry("exclude patterns", func(files fstest.MapFS) *SchemaProvisioner {
			return NewProvisioner(files, WithDir("migrations"), WithName("query"), WithExclude("audit.*"))
		}),
		Entry("table drop policy", func(files fstest.MapFS) *SchemaProvisioner {
			return NewProvisioner(files, WithDir("migrations"), WithName("query"), WithTableDrops())
		}),
		Entry("variables", func(files fstest.MapFS) *SchemaProvisioner {
			return NewProvisioner(files, WithDir("migrations"), WithName("query"), WithVariables(map[string]cty.Value{
				"enabled": cty.True,
			}))
		}),
	)

	It("freezes options at construction", func(ctx SpecContext) {
		excludes := []string{"audit.*"}
		variables := map[string]cty.Value{"name": cty.StringVal("before")}
		provisioner := NewProvisioner(baseFS(),
			WithDir("migrations"),
			WithName("query"),
			WithExclude(excludes...),
			WithVariables(variables),
		)
		before, err := provisioner.Fingerprint(ctx)
		Expect(err).NotTo(HaveOccurred())

		excludes[0] = "changed.*"
		variables["name"] = cty.StringVal("after")
		after, err := provisioner.Fingerprint(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
	})
})
