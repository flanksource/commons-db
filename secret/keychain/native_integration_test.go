//go:build darwin && keychainintegration

package keychain

import (
	"context"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zalando/go-keyring"
)

var _ = Describe("native macOS Keychain", func() {
	It("persists a KEK across independent keeper instances", func() {
		options := Options{Service: "com.flanksource.gavel.test." + uuid.NewString(), Account: "kek", Confirm: func(context.Context) (bool, error) { return true, nil }}
		first, id, err := Open(context.Background(), options)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Close)
		DeferCleanup(func() { Expect(keyring.Delete(options.Service, options.Account)).To(Succeed()) })
		encrypted, err := first.Encrypt(context.Background(), []byte("test credential"))
		Expect(err).NotTo(HaveOccurred())
		options.Confirm = nil
		reopened, secondID, err := Open(context.Background(), options)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reopened.Close)
		value, err := reopened.Decrypt(context.Background(), encrypted)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(value)).To(Equal("test credential"))
		Expect(secondID).To(Equal(id))
	})
})
