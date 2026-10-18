package git_test

import (
	"encoding/base64"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("git command environment", func() {
	var fixture bareRepo
	BeforeEach(func() { fixture = newBareRepo("rules") })

	It("keeps a parent's GIT_TRACE* from printing the Authorization header into the error", func(ctx SpecContext) {
		remote := newHTTPRemote(fixture.Root, false)
		for _, name := range []string{"GIT_TRACE", "GIT_TRACE_CURL", "GIT_CURL_VERBOSE", "GIT_TRACE_PACKET"} {
			GinkgoT().Setenv(name, "1")
		}
		GinkgoT().Setenv("GIT_TRACE_REDACT", "0")
		missing := "0123456789abcdef0123456789abcdef01234567"
		repo := newRepository(remote.URL(fixture), git.Auth{Username: remote.User, Password: remote.Password})
		_, err := git.Clone(ctx, repo, git.CloneOptions{Dir: filepath.Join(GinkgoT().TempDir(), "clone"), Ref: missing})
		Expect(err).To(MatchError(ContainSubstring("fetch")))
		header := base64.StdEncoding.EncodeToString([]byte(remote.User + ":" + remote.Password))
		Expect(err.Error()).ToNot(ContainSubstring(header))
		Expect(err.Error()).ToNot(ContainSubstring("Authorization"))
	})

	It("ignores the user's global and system git config (insteadOf, hooks, gpgsign)", func(ctx SpecContext) {
		hooks := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755)).To(Succeed())
		global := filepath.Join(GinkgoT().TempDir(), "gitconfig")
		Expect(os.WriteFile(global, []byte("[url \"file:///nonexistent/\"]\n\tinsteadOf = "+fixture.FileURL()+"\n"+
			"[core]\n\thooksPath = "+hooks+"\n[commit]\n\tgpgsign = true\n[user]\n\tsigningkey = missing\n"), 0o600)).To(Succeed())
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", global)
		GinkgoT().Setenv("GIT_CONFIG_SYSTEM", global)

		repo := newRepository(fixture.FileURL(), git.Auth{})
		refs, err := repo.LsRemote(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(refs.Head.SHA).To(Equal(fixture.Main))

		cache := &git.Cache{Root: GinkgoT().TempDir()}
		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", Branch: "apply/isolated", Message: "isolated",
			Author: fixtureSignature, Files: []git.FileChange{{Path: "new.xml", Content: []byte("<rule/>")}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.CommitSHA).ToNot(BeEmpty())
	})
})
