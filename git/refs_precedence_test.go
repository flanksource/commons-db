package git_test

import (
	"strings"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("remote ref precedence", func() {
	branch := git.Ref{Name: "release", FullName: "refs/heads/release", SHA: strings.Repeat("1", 40), Kind: git.RefBranch}
	tag := git.Ref{Name: "release", FullName: "refs/tags/release", SHA: strings.Repeat("2", 40), Kind: git.RefTag}

	DescribeTable("resolves the advertised ref",
		func(refs git.RemoteRefs, name string, want git.Ref) {
			ref, err := refs.Resolve(name)
			Expect(err).NotTo(HaveOccurred())
			Expect(ref).To(Equal(want))
		},
		Entry("branch only", git.RemoteRefs{Branches: []git.Ref{branch}}, "release", branch),
		Entry("tag only", git.RemoteRefs{Tags: []git.Ref{tag}}, "release", tag),
		Entry("branch before same-named tag", git.RemoteRefs{Branches: []git.Ref{branch}, Tags: []git.Ref{tag}}, "release", branch),
		Entry("explicit branch", git.RemoteRefs{Branches: []git.Ref{branch}, Tags: []git.Ref{tag}}, branch.FullName, branch),
		Entry("explicit tag", git.RemoteRefs{Branches: []git.Ref{branch}, Tags: []git.Ref{tag}}, tag.FullName, tag),
	)

	DescribeTable("rejects an absent explicit or short ref",
		func(refs git.RemoteRefs, name string) {
			_, err := refs.Resolve(name)
			Expect(err).To(MatchError(ContainSubstring("not found")))
		},
		Entry("neither branch nor tag", git.RemoteRefs{}, "release"),
		Entry("explicit missing branch does not select tag", git.RemoteRefs{Tags: []git.Ref{tag}}, branch.FullName),
		Entry("explicit missing tag does not select branch", git.RemoteRefs{Branches: []git.Ref{branch}}, tag.FullName),
	)
})
