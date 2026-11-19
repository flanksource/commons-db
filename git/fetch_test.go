package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func branchRef(name string) git.Ref {
	return git.Ref{Name: name, FullName: "refs/heads/" + name, Kind: git.RefBranch}
}

func shaRef(sha string) git.Ref {
	return git.Ref{Name: sha, FullName: sha, SHA: sha, Kind: git.RefSHA}
}

var _ = Describe("Cache.Fetch", func() {
	It("fetches a branch, a tag and a bare SHA in one negotiation", func(ctx SpecContext) {
		origin := newBareRepo("origin")
		remote := newHTTPRemote(origin.Root, false)
		repo := newRepository(remote.URL(origin), git.Auth{Username: remote.User, Password: remote.Password})
		Expect((&git.Cache{Root: fixtureTempDir()}).Fetch(ctx, repo, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		single := remote.uploadPacks()

		cache := &git.Cache{Root: fixtureTempDir()}
		tag := git.Ref{Name: "v1", FullName: "refs/tags/v1", Kind: git.RefTag}
		Expect(cache.Fetch(ctx, repo, git.FetchOptions{Refs: []git.Ref{branchRef("main"), tag, shaRef(origin.Feature)}})).To(Succeed())
		Expect(remote.uploadPacks()).To(Equal(2*single), "three refs cost the upload-pack requests of one")

		for _, sha := range []string{origin.Main, origin.First, origin.Feature} {
			_, err := cache.Files(ctx, repo, sha)
			Expect(err).ToNot(HaveOccurred(), sha)
		}
		Expect(remote.uploadPacks()).To(Equal(2*single), "every fetched commit is read from the mirror")
	})
})
