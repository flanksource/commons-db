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

func (h unconfirmedLease) renew(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

func (h unconfirmedLease) unlock(context.Context) error {
	close(h.removed)
	return nil
}

var _ = Describe("lease lifecycle", func() {
	It("signals loss before expiry when renewal cannot confirm ownership", func(ctx SpecContext) {
		const ttl = 300 * time.Millisecond
		l := locker{options: Options{OperationTimeout: time.Second}, ttl: ttl}
		held := unconfirmedLease{removed: make(chan struct{})}
		lease := l.manage(ctx, held, time.Now())
		Eventually(lease.Done(), ttl).Should(BeClosed())
		Expect(errors.Is(lease.Err(), ErrLost)).To(BeTrue())
		Expect(lease.Unlock(ctx)).To(Succeed())
		Expect(lease.Unlock(ctx)).To(Succeed())
		Expect(held.removed).To(BeClosed())
	}, SpecTimeout(2*time.Second))
})
