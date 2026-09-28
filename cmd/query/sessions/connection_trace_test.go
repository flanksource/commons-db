package sessions

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("connection trace provider options", func() {
	ginkgo.It("scopes to the database the form named, as a database pattern", func() {
		options := connectionTraceInput{Database: " warehouse ", Users: []string{"svc"}}.providerOptions("trace")

		Expect(options).To(HaveKeyWithValue("databases", []string{"warehouse"}))
		Expect(options).To(HaveKeyWithValue("sessionName", "trace"))
		Expect(options).To(HaveKeyWithValue("users", []string{"svc"}))
		Expect(options).NotTo(HaveKey("database"))
	})

	ginkgo.It("leaves the scope to the connection's own database when the form names none", func() {
		options := connectionTraceInput{Database: "  "}.providerOptions("trace")

		Expect(options).NotTo(HaveKey("databases"))
	})
})
