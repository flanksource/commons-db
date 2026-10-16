package main

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/mod/modfile"
)

func TestReleaseModules(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Release modules")
}

var _ = Describe("rewriteModuleRequirements", func() {
	const module = "github.com/flanksource/commons-db/cmd/query"
	var dependencies = []string{
		"github.com/flanksource/commons-db",
		"github.com/flanksource/commons-db/recordstore",
		"github.com/flanksource/commons-db/tracing",
	}

	It("sets every sibling requirement to the release version and preserves other requirements", func() {
		input := []byte("module " + module + "\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/flanksource/commons-db v0.1.50\n\tgithub.com/flanksource/commons-db/recordstore v0.1.45\n\tgithub.com/flanksource/commons-db/tracing v0.1.45\n\tgithub.com/google/uuid v1.6.0\n)\n")
		expected := "module " + module + "\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/flanksource/commons-db v0.1.58\n\tgithub.com/flanksource/commons-db/recordstore v0.1.58\n\tgithub.com/flanksource/commons-db/tracing v0.1.58\n\tgithub.com/google/uuid v1.6.0\n)\n"

		updated, err := rewriteModuleRequirements(input, "v0.1.58", module, dependencies)

		Expect(err).NotTo(HaveOccurred())
		Expect(string(updated)).To(Equal(expected))
	})

	It("rejects a missing sibling requirement", func() {
		input := []byte("module " + module + "\n\ngo 1.26.1\n\nrequire github.com/flanksource/commons-db v0.1.50\n")

		_, err := rewriteModuleRequirements(input, "v0.1.58", module, dependencies)

		Expect(err).To(MatchError(ContainSubstring("github.com/flanksource/commons-db/recordstore")))
	})

	It("rejects a different module and invalid version", func() {
		input := []byte("module github.com/example/other\n\ngo 1.26.1\n")

		_, err := rewriteModuleRequirements(input, "v0.1.58", module, dependencies)
		Expect(err).To(MatchError(ContainSubstring(module)))

		_, err = rewriteModuleRequirements(input, "not-a-version", module, dependencies)
		Expect(err).To(MatchError(ContainSubstring("not-a-version")))
	})

	It("updates the requirements in every repository module", func() {
		const version = "v0.1.58"
		for _, module := range modules {
			original, err := os.ReadFile(filepath.Join("..", "..", "..", module.file))
			Expect(err).NotTo(HaveOccurred())

			updated, err := rewriteModuleRequirements(original, version, module.module, module.dependencies)
			Expect(err).NotTo(HaveOccurred())
			parsed, err := modfile.Parse(module.file, updated, nil)
			Expect(err).NotTo(HaveOccurred())
			versions := make(map[string]string, len(parsed.Require))
			for _, requirement := range parsed.Require {
				versions[requirement.Mod.Path] = requirement.Mod.Version
			}
			for _, dependency := range module.dependencies {
				Expect(versions).To(HaveKeyWithValue(dependency, version))
			}
		}
	})
})
