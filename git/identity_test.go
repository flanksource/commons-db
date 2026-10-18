package git_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DefaultIdentity", func() {
	useGlobalConfig := func(content string) {
		global := filepath.Join(GinkgoT().TempDir(), "gitconfig")
		Expect(os.WriteFile(global, []byte(content), 0o600)).To(Succeed())
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", global)
	}

	It("reads user.name and user.email from the process's global git config", func(ctx SpecContext) {
		useGlobalConfig("[user]\n\tname = Apply Server\n\temail = server@example.org\n")
		Expect(git.DefaultIdentity(ctx)).To(Equal(git.Signature{Name: "Apply Server", Email: "server@example.org"}))
	})

	DescribeTable("refuses an incomplete identity",
		func(ctx SpecContext, content string) {
			useGlobalConfig(content)
			_, err := git.DefaultIdentity(ctx)
			Expect(err).To(MatchError(git.ErrNoIdentity))
		},
		Entry("no user section", "[core]\n\tbare = false\n"),
		Entry("a name without an email", "[user]\n\tname = Apply Server\n"),
		Entry("an email without a name", "[user]\n\temail = server@example.org\n"),
	)
})
