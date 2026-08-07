package query_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"github.com/flanksource/commons-db/query"

	// The shipped profiles reference library processors by name, which only
	// resolve once the built-in processors have registered themselves.
	_ "github.com/flanksource/commons-db/query/processor"
)

// The profiles/ directory is the worked-example set — the thing a new author
// copies. An example that does not parse and validate is worse than no example,
// so every one of them is checked here rather than trusted.
var _ = Describe("shipped example profiles", func() {
	paths, err := filepath.Glob("../profiles/*.yaml")
	Expect(err).ToNot(HaveOccurred())
	Expect(paths).ToNot(BeEmpty())

	for _, path := range paths {
		It("parses and validates "+filepath.Base(path), func() {
			body, err := os.ReadFile(path)
			Expect(err).ToNot(HaveOccurred())

			var profile query.Profile
			Expect(yaml.Unmarshal(body, &profile)).To(Succeed())
			Expect(profile.Name).ToNot(BeEmpty())
			Expect(profile.Validate()).To(Succeed())
		})
	}
})

var _ = Describe("the Java application logs example", func() {
	It("wires the stack trace merge in by library name, not by restating it", func() {
		body, err := os.ReadFile("../profiles/java-app-logs.yaml")
		Expect(err).ToNot(HaveOccurred())

		var profile query.Profile
		Expect(yaml.Unmarshal(body, &profile)).To(Succeed())
		Expect(profile.Processors).To(HaveLen(1))
		Expect(profile.Processors[0].Use).To(Equal("java.stacktrace"))
		Expect(profile.Processors[0].Config).To(BeEmpty())

		resolved, err := profile.Processors[0].Resolve()
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.Type).To(Equal("cel.batch"))
		Expect(resolved.Config).To(HaveKey("continuation"))
	})
})
