package cli

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("secret value output", func() {
	It("uses secret wrappers for logs, JSON and terminal output", func() {
		const password = "password-value-canary"
		const token = "token-value-canary"
		value := map[string]any{"name": "example", "password": password, "properties": map[string]any{"token": token}, "url": "https://example.com?token=" + token}
		Redact(value)
		for _, format := range []string{"%v", "%+v", "%#v"} {
			rendered := fmt.Sprintf(format, value)
			Expect(rendered).NotTo(ContainSubstring(password))
			Expect(rendered).NotTo(ContainSubstring(token))
		}
		encoded, err := json.Marshal(value)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring(password))
		Expect(string(encoded)).NotTo(ContainSubstring(token))
		for _, format := range []string{"json", "yaml", "pretty", "markdown"} {
			rendered, err := clicky.Format(value, clicky.FormatOptions{Format: format})
			Expect(err).NotTo(HaveOccurred())
			Expect(rendered).NotTo(ContainSubstring(password))
			Expect(rendered).NotTo(ContainSubstring(token))
		}
	})
})
