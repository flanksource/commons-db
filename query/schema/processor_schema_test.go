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
		Expect(use["enum"]).To(ContainElements("logs.json", "logs.logfmt"))
		Expect(use["enum"]).To(ContainElement("java.stacktrace"))
		Expect(use["x-enum-labels"]).To(HaveKeyWithValue("logs.json", "Parse JSON logs"))
		Expect(use["x-enum-labels"]).To(HaveKeyWithValue("logs.logfmt", "Parse logfmt logs"))
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

	processors := func() schema.Schema {
		return schema.ProfileSource()["properties"].(schema.Schema)["processors"].(schema.Schema)
	}

	It("asks for the pipeline widget, since order is semantic and config is untyped", func() {
		Expect(processors()["x-clicky-component"]).To(Equal("processor-pipeline"))
	})

	// Without this an editor can show what the author typed but not what runs.
	It("carries what each library preset resolves to, not just its name", func() {
		presets, ok := processors()["x-clicky-presets"].(schema.Schema)
		Expect(ok).To(BeTrue())
		Expect(presets).To(HaveKey("logs.json"))
		Expect(presets).To(HaveKey("logs.logfmt"))
		Expect(presets).To(HaveKey("java.stacktrace"))

		jsonLogs := presets["logs.json"].(schema.Schema)
		Expect(jsonLogs["type"]).To(Equal("logs.parse"))
		Expect(jsonLogs["config"]).To(HaveKeyWithValue("format", "json"))

		java, ok := presets["java.stacktrace"].(schema.Schema)
		Expect(ok).To(BeTrue())
		Expect(java["type"]).To(Equal("cel.batch"))

		config, ok := java["config"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(config).To(HaveKey("continuation"))
		Expect(config).To(HaveKeyWithValue("keep", "first"))
		Expect(config["set"]).To(HaveKey("stack_depth"))
	})

	It("names every registered preset, so the widget never meets an unknown `use`", func() {
		presets := processors()["x-clicky-presets"].(schema.Schema)

		for _, name := range query.NamedProcessorNames() {
			Expect(presets).To(HaveKey(name))
		}
	})
})
