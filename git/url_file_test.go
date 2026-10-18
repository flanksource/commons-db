package git_test

import (
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RepoURL.FileURL", func() {
	const (
		commit = "c5b6b41b74c1572e0915446f12b7ac1b7cb2a5f3"
		file   = "rules/pc/Acme Holdings/pl/Customer Plan/tr/AddBenefit/data.xml"
	)

	DescribeTable("links a file at a commit on the provider's web view",
		func(raw, hint, expected string) {
			repo, err := git.ParseRepoURL(raw, hint)
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.FileURL(git.Ref{SHA: commit, Kind: git.RefSHA}, file)).To(Equal(expected))
		},
		Entry("GitHub HTTPS", "https://github.com/acme/widgets.git", "",
			"https://github.com/acme/widgets/blob/"+commit+"/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
		Entry("GitHub scp", "git@github.com:acme/widgets.git", "",
			"https://github.com/acme/widgets/blob/"+commit+"/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
		Entry("self-hosted GitLab with nested groups", "https://gitlab.example.com:8443/acme/platform/cli.git", models.ConnectionTypeGitlab,
			"https://gitlab.example.com:8443/acme/platform/cli/-/blob/"+commit+"/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
		Entry("Azure dev.azure.com with user", "https://acme@dev.azure.com/acme/Platform/_git/rules", "",
			"https://dev.azure.com/acme/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GC"+commit),
		Entry("Azure ssh v3", "git@ssh.dev.azure.com:v3/acme/Platform/rules", "",
			"https://dev.azure.com/acme/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GC"+commit),
		Entry("Azure visualstudio.com DefaultCollection", "https://acme.visualstudio.com/DefaultCollection/Platform/_git/rules", "",
			"https://acme.visualstudio.com/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GC"+commit),
		Entry("Azure DevOps Server", "https://tfs.example.com/tfs/Collection/Platform/_git/rules", models.ConnectionTypeAzureDevops,
			"https://tfs.example.com/tfs/Collection/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GC"+commit),
	)

	DescribeTable("links a file on a branch or tag by its name",
		func(raw, hint string, ref git.Ref, expected string) {
			repo, err := git.ParseRepoURL(raw, hint)
			Expect(err).ToNot(HaveOccurred())
			Expect(repo.FileURL(ref, file)).To(Equal(expected))
		},
		Entry("GitHub branch", "https://github.com/acme/widgets.git", "", git.Ref{Name: "release/v2", SHA: commit, Kind: git.RefBranch},
			"https://github.com/acme/widgets/blob/release/v2/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
		Entry("GitLab tag", "https://gitlab.example.com/acme/cli.git", models.ConnectionTypeGitlab, git.Ref{Name: "v1.0", SHA: commit, Kind: git.RefTag},
			"https://gitlab.example.com/acme/cli/-/blob/v1.0/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
		Entry("Azure branch", "https://dev.azure.com/acme/Platform/_git/rules", "", git.Ref{Name: "release/v2", SHA: commit, Kind: git.RefBranch},
			"https://dev.azure.com/acme/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GBrelease%2Fv2"),
		Entry("Azure tag", "https://dev.azure.com/acme/Platform/_git/rules", "", git.Ref{Name: "v1.0", SHA: commit, Kind: git.RefTag},
			"https://dev.azure.com/acme/Platform/_git/rules?path=%2Frules%2Fpc%2FAcme+Holdings%2Fpl%2FCustomer+Plan%2Ftr%2FAddBenefit%2Fdata.xml&version=GTv1.0"),
		Entry("a detached remote HEAD links its commit", "https://github.com/acme/widgets.git", "", git.Ref{Name: "HEAD", SHA: commit, Kind: git.RefHead},
			"https://github.com/acme/widgets/blob/"+commit+"/rules/pc/Acme%20Holdings/pl/Customer%20Plan/tr/AddBenefit/data.xml"),
	)

	DescribeTable("refuses what has no web file view",
		func(raw, hint string, ref git.Ref, gotFile, message string) {
			repo, err := git.ParseRepoURL(raw, hint)
			Expect(err).ToNot(HaveOccurred())
			_, err = repo.FileURL(ref, gotFile)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("generic git host", "https://git.example.com/acme/rules.git", "", git.Ref{SHA: commit, Kind: git.RefSHA}, file, git.ErrNoFileView.Error()),
		Entry("local repository", "/srv/git/rules.git", "", git.Ref{SHA: commit, Kind: git.RefSHA}, file, git.ErrNoFileView.Error()),
		Entry("missing commit", "https://github.com/acme/widgets.git", "", git.Ref{Kind: git.RefSHA}, file, "commit is required"),
		Entry("a branch without a name", "https://github.com/acme/widgets.git", "", git.Ref{SHA: commit, Kind: git.RefBranch}, file, "branch name is required"),
		Entry("missing path", "https://github.com/acme/widgets.git", "", git.Ref{SHA: commit, Kind: git.RefSHA}, "", "path is required"),
	)
})
