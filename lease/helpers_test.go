package lease

import (
	. "github.com/onsi/gomega"
)

func as(name string) LockOptions { return LockOptions{Holder: name} }

func expectBusy(l Lease, acquired bool, err error) {
	Expect(err).NotTo(HaveOccurred())
	Expect(l).To(BeNil())
	Expect(acquired).To(BeFalse())
}
