package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConnectionSecrets(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Connection secret output")
}

var _ = Describe("connection secret output", func() {
	It("redacts diagnostic formats and structured logs while preserving explicit serialization", func() {
		const canary = "connection-secret-canary"
		c := Connection{Name: "example", Type: "http", URL: "https://user:" + canary + "@example.com", Password: canary, Certificate: canary, Properties: map[string]string{"custom": canary}}
		for _, format := range []string{"%s", "%q", "%v", "%+v", "%#v", "%x"} {
			Expect(fmt.Sprintf(format, c)).NotTo(ContainSubstring(canary))
		}
		for _, jsonOutput := range []bool{false, true} {
			var output bytes.Buffer
			var handler slog.Handler = slog.NewTextHandler(&output, nil)
			if jsonOutput {
				handler = slog.NewJSONHandler(&output, nil)
			}
			slog.New(handler).LogAttrs(context.Background(), slog.LevelInfo, "connection", slog.Any("value", c))
			Expect(output.String()).NotTo(ContainSubstring(canary))
		}
		data, err := json.Marshal(c)
		Expect(err).NotTo(HaveOccurred())
		var restored Connection
		Expect(json.Unmarshal(data, &restored)).To(Succeed())
		Expect(restored.Password).To(Equal(canary))
		Expect(restored.Properties).To(Equal(c.Properties))
	})
})
