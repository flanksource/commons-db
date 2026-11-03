package readcache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons-db/git/readcache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type failingStore struct {
	readcache.Store
	readErr, writeErr error
}

type blockingWriteStore struct {
	readcache.Memory
	started, release chan struct{}
}

func (s *blockingWriteStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	close(s.started)
	select {
	case <-s.release:
		return s.Memory.Set(ctx, key, value, ttl)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s failingStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.readErr != nil {
		return nil, false, s.readErr
	}
	return s.Store.Get(ctx, key)
}

func (s failingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.Store.Set(ctx, key, value, ttl)
}

var _ = Describe("cache failure and lifetime isolation", func() {
	It("cancels invalidation waiting for an active backend write", func(ctx SpecContext) {
		store := &blockingWriteStore{started: make(chan struct{}), release: make(chan struct{})}
		cache := &readcache.Cache{Store: store}
		release := sync.OnceFunc(func() { close(store.release) })
		defer release()
		loaded := make(chan error, 1)
		go func() {
			_, err := cache.Load(ctx, "refs", time.Hour, func(context.Context) ([]byte, error) { return []byte("value"), nil })
			loaded <- err
		}()
		Eventually(store.started).Should(BeClosed())
		bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- cache.Invalidate(bounded, "refs") }()
		Eventually(done).Should(Receive(MatchError(context.DeadlineExceeded)))
		Consistently(loaded, 20*time.Millisecond).ShouldNot(Receive())
		release()
		Eventually(loaded).Should(Receive(BeNil()))
	})

	It("does not let a read started before invalidation overwrite the refreshed value", func(ctx SpecContext) {
		cache := &readcache.Cache{Store: &readcache.Memory{}}
		started, release := make(chan struct{}), make(chan struct{})
		finish := sync.OnceFunc(func() { close(release) })
		defer finish()
		done := make(chan error, 1)
		go func() {
			_, err := cache.Load(ctx, "refs", time.Hour, func(context.Context) ([]byte, error) {
				close(started)
				<-release
				return []byte("old"), nil
			})
			done <- err
		}()
		Eventually(started).Should(BeClosed())
		Expect(cache.Invalidate(ctx, "refs")).To(Succeed())
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		fresh, err := cache.Load(bounded, "refs", time.Hour, func(context.Context) ([]byte, error) { return []byte("fresh"), nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(string(fresh)).To(Equal("fresh"))
		finish()
		Eventually(done).Should(Receive(BeNil()))
		value, found, err := cache.Store.Get(ctx, "refs")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(string(value)).To(Equal("fresh"))
	})

	It("retries a canceled owner using the waiting caller's context", func(ctx SpecContext) {
		cache := &readcache.Cache{Store: &readcache.Memory{}}
		owner, cancel := context.WithCancel(ctx)
		defer cancel()
		started, joined := make(chan struct{}), make(chan struct{})
		ownerDone, waiterDone := make(chan error, 1), make(chan error, 1)
		go func() {
			_, err := cache.Load(owner, "shared", time.Hour, func(ctx context.Context) ([]byte, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			ownerDone <- err
		}()
		Eventually(started).Should(BeClosed())
		waiter := readcache.WithObserver(ctx, func(_ context.Context, event readcache.Event) {
			if event.Disposition == "joined" && !event.Done {
				close(joined)
			}
		})
		go func() {
			_, err := cache.Load(waiter, "shared", time.Hour, func(ctx context.Context) ([]byte, error) {
				return []byte("waiter completed"), ctx.Err()
			})
			waiterDone <- err
		}()
		Eventually(joined).Should(BeClosed())
		cancel()
		Eventually(ownerDone).Should(Receive(MatchError(context.Canceled)))
		Eventually(waiterDone).Should(Receive(BeNil()))
		value, found, err := cache.Store.Get(ctx, "shared")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(string(value)).To(Equal("waiter completed"))
	})

	DescribeTable("surfaces backend failures without falling back",
		func(readFailure bool) {
			failure := errors.New("cache backend unavailable")
			store := failingStore{Store: &readcache.Memory{}}
			if readFailure {
				store.readErr = failure
			} else {
				store.writeErr = failure
			}
			loads := 0
			_, err := (&readcache.Cache{Store: store}).Load(context.Background(), "key", time.Hour, func(context.Context) ([]byte, error) {
				loads++
				return []byte("value"), nil
			})
			Expect(errors.Is(err, failure)).To(BeTrue())
			if readFailure {
				Expect(loads).To(BeZero())
			} else {
				Expect(loads).To(Equal(1))
			}
		}, Entry("read failure", true), Entry("write failure", false))

	It("expires filesystem values and rejects corrupt cache entries", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		store := readcache.Filesystem{Root: root}
		Expect(store.Set(ctx, "entry", []byte("stored"), time.Millisecond)).To(Succeed())
		Eventually(func() bool { _, found, err := store.Get(ctx, "entry"); Expect(err).NotTo(HaveOccurred()); return found }).Should(BeFalse())
		Expect(store.Set(ctx, "entry", []byte("stored"), time.Hour)).To(Succeed())
		entries, err := filepath.Glob(filepath.Join(root, "*.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(os.WriteFile(entries[0], []byte("corrupt"), 0o600)).To(Succeed())
		_, _, err = store.Get(ctx, "entry")
		Expect(err).To(MatchError(ContainSubstring("decode")))
	})
})
