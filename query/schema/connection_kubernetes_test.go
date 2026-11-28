// Tests the Kubernetes selection a connection form offers: its layout, and which types offer it
// when a policy narrows the value sources a connection may use.
package schema_test

import (
	"strings"

	"github.com/flanksource/commons-db/query/schema"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kubernetes selection layout", func() {
	It("keeps the selectors together before logging without narrowing other provider fields", func() {
		component := schema.ConnectionComponents()["kubernetes"]["properties"].(schema.Schema)
		properties := component["properties"].(schema.Schema)
		Expect(properties).To(HaveKeyWithValue("x-columns", 2))
		Expect(properties).To(HaveKeyWithValue("x-clicky-order", 7))
		fields := properties["properties"].(schema.Schema)
		Expect(fields["kubeconfig"].(schema.Schema)).NotTo(HaveKeyWithValue("x-col-span", "full"))
		Expect(fields["kubecontext"].(schema.Schema)).NotTo(HaveKeyWithValue("x-col-span", "full"))
		Expect(fields["max_resources"].(schema.Schema)).To(HaveKeyWithValue("x-col-span", "full"))
	})

	It("gives every conditional authentication field its own row beside the selectors", func() {
		component := schema.ConnectionComponents()["opensearch"]["properties"].(schema.Schema)
		properties := component["properties"].(schema.Schema)
		Expect(properties).To(HaveKeyWithValue("x-columns", 2))
		credentials := map[string]any{}
		for _, branch := range properties["allOf"].([]any) {
			for name, field := range branch.(schema.Schema)["then"].(schema.Schema)["properties"].(schema.Schema) {
				credentials[name] = field.(schema.Schema)["x-col-span"]
			}
		}
		Expect(credentials).To(HaveKeyWithValue("username", "full"))
		Expect(credentials).To(HaveKeyWithValue("password", "full"))
		for name, span := range credentials {
			Expect(span).To(Equal("full"), name)
		}
	})
})

// kubernetesSelectionTypes are the connection types whose form in bundled offers a kubeconfig.
func kubernetesSelectionTypes(bundled schema.Schema) []string {
	var types []string
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if title, ok := n["title"].(string); ok && strings.HasPrefix(title, "Connection: ") {
				properties, _ := n["properties"].(map[string]any)
				nested, _ := properties["properties"].(map[string]any)
				fields, _ := nested["properties"].(map[string]any)
				if _, ok := fields["kubeconfig"]; ok {
					types = append(types, strings.TrimPrefix(title, "Connection: "))
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(map[string]any(bundled))
	return types
}

var _ = Describe("Kubernetes selection under a policy", func() {
	It("is offered by every type when values may come from the cluster", func() {
		Expect(kubernetesSelectionTypes(schema.Connection(schema.ConnectionOptions{}))).To(ContainElements("gitlab", "postgres", "kubernetes"))
		Expect(kubernetesSelectionTypes(schema.Connection(schema.ConnectionOptions{SecretSources: []string{"secret"}, URLSources: []string{"value"}}))).
			To(ContainElements("gitlab", "postgres", "kubernetes"))
	})

	It("is offered only by the kubernetes type when every value must be literal", func() {
		literal := schema.ConnectionOptions{AllowedTypes: []string{"gitlab", "kubernetes"}, SecretSources: []string{"value"}, URLSources: []string{"value"}}
		Expect(kubernetesSelectionTypes(schema.Connection(literal))).To(ConsistOf("kubernetes"))
		Expect(kubernetesSelectionTypes(schema.Connection(schema.ConnectionOptions{SecretSources: []string{"value", "onepassword"}, URLSources: []string{"value"}}))).
			To(ConsistOf("kubernetes"), "1Password isn't read through a cluster")
	})
})
