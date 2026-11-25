package lease

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("lease names", func() {
	It("names SQL Server app locks by scope and key within the resource limit", func() {
		Expect(sqlResource(Request{Scope: "deploy", Key: "git-azure_oma"})).To(Equal("deploy:git-azure_oma"))
		longest := strings.Repeat("a", maxNameLength)
		name, err := sqlResource(Request{Scope: longest, Key: longest})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(name + sqlLongestSuffix)).To(BeNumerically("<=", sqlResourceLimit))
	})
})
