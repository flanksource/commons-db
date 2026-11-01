package git_test

import (
	"errors"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("empty replay patches", func() {
	DescribeTable("honors AllowEmpty and never pushes a failed replay",
		func(ctx SpecContext, initiallyEmpty, allowEmpty bool) {
			upstream := newBareRepo("rules")
			fork := newFork(upstream)
			var empty string
			if initiallyEmpty {
				gitFixture(fork.work(), "commit", "--allow-empty", "-m", "Audit checkpoint")
				gitFixture(fork.work(), "push", "-q", fork.Path, "main")
				empty = gitFixture(fork.work(), "rev-parse", "HEAD")
			} else {
				empty = fork.commitOn(fixtureCommit{Branch: "main", Path: "applied.txt", Content: "already applied\n", Message: "Add shared rule"})
				upstream.commitOn(fixtureCommit{Branch: "main", Path: "applied.txt", Content: "already applied\n", Message: "Apply shared rule independently"})
			}
			next := fork.commitOn(fixtureCommit{Branch: "main", Path: "next.txt", Content: "next\n", Message: "Add next rule"})
			cache := &git.Cache{Root: GinkgoT().TempDir()}
			result, err := cache.Apply(ctx, newRepository(upstream.FileURL(), git.Auth{}), git.ChangeRequest{
				Base: "main", Branch: "promote/replay", Author: fixtureSignature,
				Source: newRepository(fork.FileURL(), git.Auth{}), Pick: git.PickReplay,
				CherryPick: []string{empty, next}, AllowEmpty: allowEmpty,
			})
			if !allowEmpty {
				var emptyErr *git.CherryPickEmptyError
				Expect(errors.As(err, &emptyErr)).To(BeTrue(), "expected CherryPickEmptyError, got %v", err)
				Expect(emptyErr).To(Equal(&git.CherryPickEmptyError{Commit: empty, Base: "main"}))
				Expect(gitFixture(upstream.Path, "branch", "--list", "promote/replay")).To(BeEmpty())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(result.SkippedCherryPicks).To(Equal([]string{empty}))
			Expect(gitFixture(upstream.Path, "rev-list", "--count", "main..promote/replay")).To(Equal("1"))
			Expect(gitFixture(upstream.Path, "show", "promote/replay:next.txt")).To(Equal("next"))
		},
		Entry("rejects an initially empty commit", true, false),
		Entry("skips an initially empty commit and continues", true, true),
		Entry("rejects an already-applied patch", false, false),
		Entry("skips an already-applied patch and continues", false, true),
	)

	DescribeTable("preserves conflict errors even when empty commits may be skipped",
		func(ctx SpecContext, allowEmpty bool) {
			upstream := newBareRepo("rules")
			fork := newFork(upstream)
			commit := fork.commitOn(fixtureCommit{Branch: "main", Path: "README.md", Content: "fork rule\n", Message: "Change shared rule"})
			upstream.commitOn(fixtureCommit{Branch: "main", Path: "README.md", Content: "upstream rule\n", Message: "Change shared rule independently"})
			cache := &git.Cache{Root: GinkgoT().TempDir()}
			_, err := cache.Apply(ctx, newRepository(upstream.FileURL(), git.Auth{}), git.ChangeRequest{
				Base: "main", Branch: "promote/conflict", Author: fixtureSignature,
				Source: newRepository(fork.FileURL(), git.Auth{}), Pick: git.PickReplay,
				CherryPick: []string{commit}, AllowEmpty: allowEmpty,
			})
			var conflict *git.CherryPickConflictError
			Expect(errors.As(err, &conflict)).To(BeTrue(), "expected CherryPickConflictError, got %v", err)
			Expect(conflict).To(Equal(&git.CherryPickConflictError{Commit: commit, Files: []string{"README.md"}}))
			Expect(gitFixture(upstream.Path, "branch", "--list", "promote/conflict")).To(BeEmpty())
		},
		Entry("AllowEmpty false", false),
		Entry("AllowEmpty true", true),
	)
})
