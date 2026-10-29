package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"math/rand"
	"net/http"
)

type packResponseWriter struct {
	http.ResponseWriter
	bytes int64
}

func (w *packResponseWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}
func (w *packResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (h *httpRemote) PackBytes() int64 { h.mu.Lock(); defer h.mu.Unlock(); return h.packBytes }

var _ = Describe("shared cache transfer", func() {
	It("transfers less pack data when a fork reuses cached objects", func(ctx SpecContext) {
		donor := newBareRepo("large-donor")
		payload := make([]byte, 512*1024)
		_, err := rand.New(rand.NewSource(7)).Read(payload)
		Expect(err).NotTo(HaveOccurred())
		donor.Main = donor.commitOn(fixtureCommit{Branch: "main", Path: "payload.bin", Content: string(payload), Message: "payload"})
		fork := newFork(donor)
		fork.Main = fork.commitOn(fixtureCommit{Branch: "main", Path: "delta.txt", Content: "delta\n", Message: "delta"})
		shared := &git.Cache{Root: fixtureTempDir()}
		initial, err := shared.Checkout(ctx, mirroredRepository(donor), git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(initial.Release()).To(Succeed())
		remote := newHTTPRemote(fork.Root, false)
		repo := newRepository(remote.URL(fork), git.Auth{Username: remote.User, Password: remote.Password})
		repo.Provider = &historyFixtureProvider{base: donor.Main}
		cold := &git.Cache{Root: fixtureTempDir()}
		baseline, err := cold.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(baseline.Release()).To(Succeed())
		coldBytes := remote.PackBytes()
		warm, err := shared.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(warm.Release()).To(Succeed())
		warmBytes := remote.PackBytes() - coldBytes
		Expect(coldBytes).To(BeNumerically(">", len(payload)))
		Expect(warmBytes).To(BeNumerically("<", coldBytes/4))
		before := remote.PackBytes()
		again, err := shared.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Release()).To(Succeed())
		Expect(remote.PackBytes()).To(Equal(before))
	})
})
