package shell

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Streaming redaction", func() {
	It("redacts values split across writes", func() {
		var output bytes.Buffer
		writer := newRedactingWriter(&output, []string{"split-sensitive-value"})

		_, err := writer.Write([]byte("before split-sensi"))
		Expect(err).ToNot(HaveOccurred())
		_, err = writer.Write([]byte("tive-value after"))
		Expect(err).ToNot(HaveOccurred())
		Expect(writer.Close()).To(Succeed())

		Expect(output.String()).To(Equal("before [REDACTED] after"))
	})
})
