package readcache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flanksource/commons-db/git/readcache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestReadCache(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Git read cache") }

type memoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *memoryStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return append([]byte(nil), v...), ok, ctx.Err()
}
func (s *memoryStore) Set(ctx context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
	return ctx.Err()
}
func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

var _ = Describe("shared immutable reads", func() {
	It("reuses a stored result across fresh cache instances and returns independent bytes", func() {
		store := &memoryStore{data: map[string][]byte{}}
		var calls int
		load := func(context.Context) ([]byte, error) { calls++; return []byte("original"), nil }
		first := &readcache.Cache{Store: store}
		value, err := first.Load(context.Background(), "commit/path", time.Hour, load)
		Expect(err).NotTo(HaveOccurred())
		value[0] = 'X'
		second := &readcache.Cache{Store: store}
		value, err = second.Load(context.Background(), "commit/path", time.Hour, load)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal([]byte("original")))
		Expect(calls).To(Equal(1))
	})
	It("does not store failures or canceled computations", func() {
		cache := &readcache.Cache{Store: &memoryStore{data: map[string][]byte{}}}
		ctx, cancel := context.WithCancel(context.Background())
		_, err := cache.Load(ctx, "canceled", time.Hour, func(context.Context) ([]byte, error) { cancel(); return []byte("partial"), nil })
		Expect(err).To(MatchError(context.Canceled))
		_, found, _ := cache.Store.Get(context.Background(), "canceled")
		Expect(found).To(BeFalse())
		failure := errors.New("provider unavailable")
		_, err = cache.Load(context.Background(), "failed", time.Hour, func(context.Context) ([]byte, error) { return nil, failure })
		Expect(err).To(MatchError(failure))
		_, found, _ = cache.Store.Get(context.Background(), "failed")
		Expect(found).To(BeFalse())
	})
	It("lets a canceled waiter leave without canceling the loader", func() {
		cache := &readcache.Cache{Store: &memoryStore{data: map[string][]byte{}}}
		started, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var calls atomic.Int32
		load := func(context.Context) ([]byte, error) {
			calls.Add(1)
			close(started)
			<-finish
			return []byte("complete"), nil
		}
		go func() { _, err := cache.Load(context.Background(), "shared", time.Hour, load); done <- err }()
		<-started
		joined := make(chan struct{})
		ctx, cancel := context.WithCancel(readcache.WithObserver(context.Background(), func(_ context.Context, event readcache.Event) {
			if event.Disposition == "joined" && !event.Done {
				close(joined)
			}
		}))
		waiterDone := make(chan error, 1)
		go func() { _, err := cache.Load(ctx, "shared", time.Hour, load); waiterDone <- err }()
		Eventually(joined).Should(BeClosed())
		cancel()
		Expect(<-waiterDone).To(MatchError(context.Canceled))
		close(finish)
		Expect(<-done).To(Succeed())
		Expect(calls.Load()).To(Equal(int32(1)))
	})
})
