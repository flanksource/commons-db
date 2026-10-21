package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worktree fingerprints", func() {
	It("is stable and changes with dirty metadata, staging, deletion, renames, HEAD and refs", func(ctx SpecContext) {
		fixture := newBareRepo("fingerprint")
		first, err := git.WorktreeFingerprint(ctx, fixture.work())
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Root).To(Equal(fixture.work()))
		Expect(first.SHA).To(HaveLen(64))
		again, err := git.WorktreeFingerprint(ctx, fixture.work())
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(first))
		path := filepath.Join(fixture.work(), "untracked [name]\n.go")
		Expect(os.WriteFile(path, []byte("first"), 0o600)).To(Succeed())
		previous := first
		for step := range 7 {
			switch step {
			case 1:
				modified := time.Now().Add(time.Second)
				Expect(os.Chtimes(path, modified, modified)).To(Succeed())
			case 2:
				gitFixture(fixture.work(), "add", filepath.Base(path))
			case 3:
				renamed := filepath.Join(fixture.work(), "renamed.go")
				gitFixture(fixture.work(), "mv", filepath.Base(path), filepath.Base(renamed))
				path = renamed
			case 4:
				Expect(os.Remove(path)).To(Succeed())
			case 5:
				gitFixture(fixture.work(), "tag", "fingerprint-tag")
			case 6:
				gitFixture(fixture.work(), "checkout", "--detach", fixture.First)
			}
			current, err := git.WorktreeFingerprint(ctx, fixture.work())
			Expect(err).NotTo(HaveOccurred())
			Expect(current.SHA).NotTo(Equal(previous.SHA))
			stable, err := git.WorktreeFingerprint(ctx, fixture.work())
			Expect(err).NotTo(HaveOccurred())
			Expect(stable).To(Equal(current))
			previous = current
		}
	})

	It("distinguishes a nonrepository from filesystem and cancellation failures", func(ctx SpecContext) {
		directory := GinkgoT().TempDir()
		_, err := git.WorktreeFingerprint(ctx, directory)
		Expect(errors.Is(err, git.ErrNotRepository)).To(BeTrue())
		_, err = git.WorktreeFingerprint(ctx, filepath.Join(directory, "missing"))
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, git.ErrNotRepository)).To(BeFalse())
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = git.WorktreeFingerprint(cancelled, directory)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, git.ErrNotRepository)).To(BeFalse())
	})

	It("ignores ignored scratch files and hashes untracked directories recursively", func(ctx SpecContext) {
		fixture := newBareRepo("fingerprint-ignore")
		writeFixtureFile(fixture.work(), ".gitignore", ".tmp/\n")
		first, err := git.WorktreeFingerprint(ctx, fixture.work())
		Expect(err).NotTo(HaveOccurred())
		writeFixtureFile(fixture.work(), ".tmp/output", "ignored")
		again, err := git.WorktreeFingerprint(ctx, fixture.work())
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(first))
		writeFixtureFile(fixture.work(), "new/nested.go", "package nested")
		changed, err := git.WorktreeFingerprint(ctx, fixture.work())
		Expect(err).NotTo(HaveOccurred())
		Expect(changed.SHA).NotTo(Equal(first.SHA))
	})
})
