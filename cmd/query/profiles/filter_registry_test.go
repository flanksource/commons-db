package profiles

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile filter registry", func() {
	filter := func(name, label string, option string) entity.NamedFilter {
		return entity.NamedFilter{
			Name: name, Label: label, Type: "multi-filter", Multi: true,
			Source: entity.StaticOptions(map[string]api.Textable{option: api.Text{Content: option}}),
		}
	}

	It("accepts an identical re-registration and refuses a different one", func() {
		const name = "profile-registry-probe-column-0"
		Expect(registerProfileFilters([]entity.NamedFilter{filter(name, "Region", "eu")})).To(Succeed())
		Expect(registerProfileFilters([]entity.NamedFilter{filter(name, "Region", "eu")})).To(Succeed())

		registered, ok := entity.GetFilter(name)
		Expect(ok).To(BeTrue())
		options, err := countedFilterOptions(registered.Source, entity.FilterContext{}, "", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(options.Options).To(HaveKey("eu"))

		Expect(registerProfileFilters([]entity.NamedFilter{filter(name, "Zone", "eu")})).
			To(MatchError(`profile filter "` + name + `" is already registered with a different definition`))
	})
})
