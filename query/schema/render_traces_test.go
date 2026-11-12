package schema_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Trace presentation", func() {
	It("offers traces independently of live trace sessions", func() {
		profile := schema.Profile()
		render := profile["properties"].(schema.Schema)["render"].(schema.Schema)
		Expect(render["enum"]).To(ContainElement("traces"))
		Expect((query.Profile{Render: query.RenderTraces}).RenderMode()).To(Equal("traces"))
	})
	It("keeps the checked-in render schemas aligned with the generator", func() {
		for _, file := range []string{"profile.json", "src/profile.json"} {
			body, err := os.ReadFile(filepath.Join("..", "..", "schemas", file))
			Expect(err).NotTo(HaveOccurred())
			var document schema.Schema
			Expect(json.Unmarshal(body, &document)).To(Succeed())
			expected, err := schema.JSON(schema.ProfileSource()["properties"].(schema.Schema)["render"].(schema.Schema))
			Expect(err).NotTo(HaveOccurred())
			actual, err := schema.JSON(document["properties"].(map[string]any)["render"].(map[string]any))
			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(MatchJSON(expected), file)
		}
	})
})
