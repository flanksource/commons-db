package lease

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type unconfirmedLease struct {
	removed chan struct{}
}

func (h unconfirmedLease) Renew(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (h unconfirmedLease) Unlock(context.Context) error {
	close(h.removed)
	return nil
}

var _ = Describe("lease lifecycle", func() {
	It("signals loss before expiry when renewal cannot confirm ownership", func(ctx SpecContext) {
		const ttl = 300 * time.Millisecond
		l := Backend{Options: Options{OperationTimeout: time.Second}, TTL: ttl}
		held := unconfirmedLease{removed: make(chan struct{})}
		lease := l.manage(ctx, held, time.Now())
		DeferCleanup(lease.Unlock, ctx)
		Eventually(lease.Done(), ttl).Should(BeClosed())
		Expect(errors.Is(lease.Err(), ErrLost)).To(BeTrue())
		Consistently(held.removed, 50*time.Millisecond).ShouldNot(BeClosed())
		Expect(lease.Unlock(ctx)).To(Succeed())
		Expect(lease.Unlock(ctx)).To(Succeed())
		Expect(held.removed).To(BeClosed())
	}, SpecTimeout(2*time.Second))
})
