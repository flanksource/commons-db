package main

import (
	"os"
	"path/filepath"
	"strings"
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

var _ = Describe("prepareRelease", func() {
	const version = "v0.1.60"
	var originalFiles map[string][]byte

	BeforeEach(func() {
		root, err := filepath.Abs(filepath.Join("..", "..", "..", ".tmp"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(root, 0o755)).To(Succeed())
		directory, err := os.MkdirTemp(root, "release-modules-test-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, directory)
		previous, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chdir(directory)).To(Succeed())
		DeferCleanup(os.Chdir, previous)

		originalFiles = make(map[string][]byte)
		for _, module := range modules {
			content := "module " + module.module + "\n\ngo 1.26.1\n\nrequire (\n"
			for _, dependency := range module.dependencies {
				content += "\t" + dependency + " v0.1.45\n"
			}
			content += ")\n"
			originalFiles[module.file] = []byte(content)
			Expect(os.MkdirAll(filepath.Dir(module.file), 0o755)).To(Succeed())
			Expect(os.WriteFile(module.file, []byte(content), 0o644)).To(Succeed())
		}
	})

	It("rejects stale requirements in check mode without modifying any files", func() {
		Expect(prepareRelease(releaseOptions{Version: version, Check: true})).To(
			MatchError(ContainSubstring("recordstore/go.mod")))
		for path, expected := range originalFiles {
			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).To(Equal(expected))
		}
	})

	It("aligns all sibling requirements and passes a subsequent read-only check", func() {
		Expect(prepareRelease(releaseOptions{Version: version})).To(Succeed())
		for path, original := range originalFiles {
			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal(strings.ReplaceAll(string(original), "v0.1.45", version)))
		}
		Expect(prepareRelease(releaseOptions{Version: version, Check: true})).To(Succeed())
	})

	It("detects a tracing requirement changed after preparation", func() {
		Expect(prepareRelease(releaseOptions{Version: version})).To(Succeed())
		const path = "tracing/go.mod"
		Expect(os.WriteFile(path, originalFiles[path], 0o644)).To(Succeed())
		Expect(prepareRelease(releaseOptions{Version: version, Check: true})).To(
			MatchError(ContainSubstring(path)))
		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal(originalFiles[path]))
	})

	It("leaves every module unchanged if a later module is invalid", func() {
		path := modules[len(modules)-1].file
		originalFiles[path] = []byte("module example.com/invalid\n\ngo 1.26.1\n")
		Expect(os.WriteFile(path, originalFiles[path], 0o644)).To(Succeed())
		Expect(prepareRelease(releaseOptions{Version: version})).To(MatchError(ContainSubstring(path)))
		for path, expected := range originalFiles {
			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).To(Equal(expected))
		}
	})
})
