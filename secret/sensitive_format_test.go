package secret

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("sensitive formatting", func() {
	It("redacts every diagnostic formatting verb", func() {
		value := Sensitive("credential-canary")
		for _, format := range []string{"%s", "%q", "%v", "%+v", "%#v", "%x", "%d"} {
			rendered := fmt.Sprintf(format, value)
			Expect(rendered).NotTo(ContainSubstring("credential-canary"))
			Expect(rendered).To(ContainSubstring("REDACTED"))
		}
	})
})
