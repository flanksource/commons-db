package readcache_test

import (
	"context"
	"time"

	"github.com/flanksource/commons-db/git/readcache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type observedStore struct {
	readcache.Memory
	started *bool
}

func (s *observedStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	Expect(*s.started).To(BeTrue(), "lookup must be visible before adapter IO")
	return s.Memory.Get(ctx, key)
}

var _ = Describe("cache lookup reporting", func() {
	It("starts before adapter IO for warm and cold reads and finishes each lookup", func() {
		started := false
		store := &observedStore{started: &started}
		cache := &readcache.Cache{Store: store}
		finishes := 0
		ctx := readcache.WithObserver(context.Background(), func(_ context.Context, event readcache.Event) {
			if event.Disposition == "lookup" && !event.Done {
				started = true
			}
			if event.Done {
				finishes++
			}
		})
		for range 2 {
			started = false
			_, err := cache.Load(ctx, "commit", time.Hour, func(context.Context) ([]byte, error) { return []byte("contents"), nil })
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(finishes).To(Equal(2))
	})
})
