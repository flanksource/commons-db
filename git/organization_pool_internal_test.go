package git

import (
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("scoped object pool identity", func(first, second, provider string, shared bool) {
	a, err := ParseRepoURL(first, provider)
	Expect(err).NotTo(HaveOccurred())
	b, err := ParseRepoURL(second, provider)
	Expect(err).NotTo(HaveOccurred())
	left, right := organizationPool(a), organizationPool(b)
	Expect(left != "" && left == right).To(Equal(shared))
},
	Entry("matches GitHub owner case and transport", "https://github.com/Example/tools.git", "git@github.com:example/docs.git", "", true),
	Entry("separates GitHub owners", "https://github.com/Example/tools.git", "https://github.com/Other/docs.git", "", false),
	Entry("separates GitHub Enterprise instances", "https://git.example.net/acme/tools.git", "https://git.other.net/acme/docs.git", models.ConnectionTypeGithub, false),
	Entry("matches Azure projects and HTTPS aliases", "https://dev.azure.com/Example/one/_git/tools", "https://example.visualstudio.com/DefaultCollection/two/_git/docs", "", true),
	Entry("matches Azure HTTPS and SSH aliases", "https://dev.azure.com/Example/one/_git/tools", "git@vs-ssh.visualstudio.com:v3/example/two/docs", "", true),
	Entry("separates Azure Server collections", "https://git.example.net/one/Project/_git/tools", "https://git.example.net/two/Project/_git/docs", models.ConnectionTypeAzureDevops, false),
	Entry("keeps generic Git in private mirrors", "https://git.example.net/acme/tools.git", "https://git.example.net/acme/docs.git", "", false),
)
