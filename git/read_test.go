package git_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type gitReadGate struct{ started, prepared, release string }

func blockGitReads() gitReadGate {
	GinkgoHelper()
	realGit, err := exec.LookPath("git")
	Expect(err).NotTo(HaveOccurred())
	root := GinkgoT().TempDir()
	gate := gitReadGate{started: filepath.Join(root, "started"), prepared: filepath.Join(root, "prepared"), release: filepath.Join(root, "release")}
	for _, file := range []string{gate.started, gate.prepared} {
		Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "update-ref" ]
then
  %q "$@"
  status=$?
  printf '1\n' >> %q
  exit "$status"
fi
for arg in "$@"
do
  if [ "$arg" = "log" ]
  then
    printf '1\n' >> %q
    while [ ! -f %q ]
    do
      sleep 0.05
    done
  fi
done
exec %q "$@"
`, realGit, gate.prepared, gate.started, gate.release, realGit)
	Expect(os.WriteFile(filepath.Join(root, "git"), []byte(script), 0o755)).To(Succeed())
	GinkgoT().Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gate
}

var _ = Describe("cached immutable reads", func() {
	It("runs four reads concurrently and cancels a queued reader without interrupting them", func(ctx SpecContext) {
		fixture := newBareRepo("concurrent")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := newRepository(fixture.FileURL(), git.Auth{})
		_, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		gate := blockGitReads()
		DeferCleanup(func() { Expect(os.WriteFile(gate.release, nil, 0o600)).To(Succeed()) })
		count := func(file string) func() int {
			return func() int {
				content, err := os.ReadFile(file)
				Expect(err).NotTo(HaveOccurred())
				return strings.Count(string(content), "1\n")
			}
		}
		finished := make(chan error, 4)
		for range 4 {
			go func() {
				_, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{Limit: 1})
				finished <- err
			}()
		}
		Eventually(count(gate.started), time.Minute, 200*time.Millisecond).Should(Equal(4))
		queued, cancel := context.WithCancel(ctx)
		defer cancel()
		cancelled := make(chan error, 1)
		go func() {
			_, err := cache.History(queued, repo, fixture.Main, git.HistoryOptions{Limit: 1})
			cancelled <- err
		}()
		Consistently(count(gate.started), time.Second, 200*time.Millisecond).Should(Equal(4))
		Expect(count(gate.prepared)()).To(BeZero(), "reads of a pinned commit rewrite no pin ref")
		cancel()
		Eventually(cancelled, time.Second).Should(Receive(MatchError(context.Canceled)))
		Expect(os.WriteFile(gate.release, nil, 0o600)).To(Succeed())
		for range 4 {
			Eventually(finished, time.Minute).Should(Receive(BeNil()))
		}
	})
})
