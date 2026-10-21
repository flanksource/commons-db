package git_test

import (
	git "github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("PR source repository identity",
	func(base, provider, source, expected string) {
		repo, err := git.ParseRepoURL(base, provider)
		Expect(err).NotTo(HaveOccurred())
		head, err := repo.SourceRepository(source)
		Expect(err).NotTo(HaveOccurred())
		Expect(head.Clone).To(Equal(expected))
	},
	Entry("GitHub fork", "https://github.com/acme/cli", models.ConnectionTypeGithub, "fork/cli", "https://github.com/fork/cli"),
	Entry("nested GitLab fork", "https://git.example/acme/cli", models.ConnectionTypeGitlab, "fork/team/cli", "https://git.example/fork/team/cli"),
	Entry("Azure project fork", "https://dev.azure.com/acme/Main/_git/cli", models.ConnectionTypeAzureDevops, "Forks/cli", "https://dev.azure.com/acme/Forks/_git/cli"),
	Entry("Azure legacy host", "https://acme.visualstudio.com/Main/_git/cli", models.ConnectionTypeAzureDevops, "Forks/cli", "https://acme.visualstudio.com/Forks/_git/cli"),
)
