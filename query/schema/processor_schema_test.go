package schema_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/schema"

	// The processor pickers are driven by the live registries, so the specs need
	// the built-in processors and library presets registered — the same blank
	// import the schema generator uses.
	_ "github.com/flanksource/commons-db/query/processor"
)

var _ = Describe("Profile processors schema", func() {
	processorItems := func() schema.Schema {
		profile := schema.ProfileSource()
		processors := profile["properties"].(schema.Schema)["processors"].(schema.Schema)
		return processors["items"].(schema.Schema)["properties"].(schema.Schema)
	}

	It("offers every registered processor type", func() {
		Expect(processorItems()["type"].(schema.Schema)["enum"]).To(Equal(query.RegisteredProcessors()))
	})

	It("offers every library preset, labelled by its title", func() {
		use := processorItems()["use"].(schema.Schema)

		Expect(use["enum"]).To(Equal(query.NamedProcessorNames()))
		Expect(use["enum"]).To(ContainElement("java.stacktrace"))
		Expect(use["x-enum-labels"]).To(HaveKeyWithValue("java.stacktrace", "Java stack trace merge"))
	})

	It("explains each preset in the field description", func() {
		use := processorItems()["use"].(schema.Schema)

		Expect(use["description"]).To(ContainSubstring("`java.stacktrace`"))
		Expect(use["description"]).To(ContainSubstring("one exception is one row"))
	})

	It("orders the library picker ahead of the raw type", func() {
		items := processorItems()

		Expect(items["use"].(schema.Schema)["x-clicky-order"]).To(Equal(0))
		Expect(items["type"].(schema.Schema)["x-clicky-order"]).To(Equal(1))
		Expect(items["config"].(schema.Schema)["x-clicky-order"]).To(Equal(2))
	})
})
