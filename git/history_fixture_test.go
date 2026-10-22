package git_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
)

// work is the non-bare checkout newBareRepo cloned the fixture from.
func (b bareRepo) work() string { return filepath.Join(b.Root, "work") }

// fixtureCommit is one Path=Content commit with Message on Branch. commitOn
// creates Branch at From when set; commitAs commits as Author at Date.
type fixtureCommit struct {
	Branch  string
	From    string
	Path    string
	Content string
	Message string
	Author  string
	Date    string
}

// commitOn commits c in the work checkout, pushes the branch to the bare
// repository and returns the new commit SHA.
func (b bareRepo) commitOn(c fixtureCommit) string {
	GinkgoHelper()
	if c.From != "" {
		gitFixture(b.work(), "checkout", "-q", "-B", c.Branch, c.From)
	} else {
		gitFixture(b.work(), "checkout", "-q", c.Branch)
	}
	writeFixtureFile(b.work(), c.Path, c.Content)
	gitFixture(b.work(), "add", "-A")
	gitFixture(b.work(), "commit", "-q", "-m", c.Message)
	gitFixture(b.work(), "push", "-q", "--force", b.Path, c.Branch)
	return gitFixture(b.work(), "rev-parse", "HEAD")
}

// changeOn runs `git change...` (a mv or rm) on branch in the work checkout,
// commits it, pushes the branch and returns the new commit SHA.
func (b bareRepo) changeOn(branch, message string, change ...string) string {
	GinkgoHelper()
	gitFixture(b.work(), "checkout", "-q", branch)
	gitFixture(b.work(), change...)
	gitFixture(b.work(), "commit", "-q", "-m", message)
	gitFixture(b.work(), "push", "-q", b.Path, branch)
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
