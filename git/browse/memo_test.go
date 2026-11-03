package browse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	rpchttp "github.com/flanksource/clicky/rpc/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Read session memo ownership", func() {
	It("distinguishes failed joined loads from completed cache hits", func() {
		var memo readMemo[int]
		ctx, timings := rpchttp.WithTimings(context.Background())
		started, finish := make(chan struct{}), make(chan struct{})
		failure := errors.New("lookup failed")
		request := memoLoad[int]{key: "failed", metric: "refs", owner: ctx, load: func() (int, error) { close(started); <-finish; return 0, failure }}
		ownerDone, waiterDone := make(chan error, 1), make(chan error, 1)
		go func() { _, err := memo.get(ctx, request); ownerDone <- err }()
		<-started
		go func() { _, err := memo.get(ctx, request); waiterDone <- err }()
		Eventually(func() int64 { count, _ := timings.Counter("release_reuse", "refs_joined"); return count }).Should(Equal(int64(1)))
		close(finish)
		Expect(<-ownerDone).To(MatchError(failure))
		Expect(<-waiterDone).To(MatchError(failure))
		hits, _ := timings.Counter("release_reuse", "refs_hits")
		Expect(hits).To(BeZero())
		request.key, request.load = "success", func() (int, error) { return 7, nil }
		for range 2 {
			value, err := memo.get(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(value).To(Equal(7))
		}
		hits, _ = timings.Counter("release_reuse", "refs_hits")
		Expect(hits).To(Equal(int64(1)))
	})
	It("cancels a waiter without discarding the owner result and retries errors", func() {
		var memo readMemo[int]
		owner, cancelOwner := context.WithCancel(context.Background())
		defer cancelOwner()
		started, finish := make(chan struct{}), make(chan struct{})
		var calls atomic.Int64
		failure := errors.New("provider unavailable")
		var once sync.Once
		request := memoLoad[int]{key: "example", metric: "refs", owner: owner, load: func() (int, error) { calls.Add(1); once.Do(func() { close(started) }); <-finish; return 0, failure }}
		var wg sync.WaitGroup
		wg.Go(func() { defer GinkgoRecover(); _, err := memo.get(owner, request); Expect(err).To(MatchError(failure)) })
		<-started
		waiter, cancel := context.WithCancel(owner)
		cancel()
		_, err := memo.get(waiter, request)
		Expect(err).To(MatchError(context.Canceled))
		close(finish)
		wg.Wait()
		_, err = memo.get(owner, request)
		Expect(err).To(MatchError(failure))
		Expect(calls.Load()).To(Equal(int64(2)))
		cancelOwner()
		_, err = memo.get(context.Background(), request)
		Expect(err).To(MatchError(context.Canceled))
	})
})
