package git_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
)

// work is the non-bare checkout newBareRepo cloned the fixture from.
func (b bareRepo) work() string { return filepath.Join(b.Root, "work") }

// commitOn commits path=content on branch (created at from when from is set)
// in the work checkout, pushes the branch to the bare repository and returns
// the new commit SHA.
func (b bareRepo) commitOn(branch, from, path, content, message string) string {
	GinkgoHelper()
	if from != "" {
		gitFixture(b.work(), "checkout", "-q", "-B", branch, from)
	} else {
		gitFixture(b.work(), "checkout", "-q", branch)
	}
	writeFixtureFile(b.work(), path, content)
	gitFixture(b.work(), "add", "-A")
	gitFixture(b.work(), "commit", "-q", "-m", message)
	gitFixture(b.work(), "push", "-q", "--force", b.Path, branch)
	return gitFixture(b.work(), "rev-parse", "HEAD")
}

// mergeInto merges source into branch with a merge commit, pushes branch and
// returns the merge commit SHA.
func (b bareRepo) mergeInto(branch, source string) string {
	GinkgoHelper()
	gitFixture(b.work(), "checkout", "-q", branch)
	gitFixture(b.work(), "merge", "-q", "--no-ff", "-m", "merge "+source, source)
	gitFixture(b.work(), "push", "-q", b.Path, branch)
	return gitFixture(b.work(), "rev-parse", "HEAD")
}
