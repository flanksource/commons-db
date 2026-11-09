package keychain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/zalando/go-keyring"
)

func TestKeychain(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Keychain KEK") }

var _ = Describe("persistent KEK", func() {
	It("keeps key documents redacted under diagnostic formatting and serialization", func() {
		const canary = "key-material-canary"
		doc := document{Version: 1, ID: "example", Key: canary}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			Expect(fmt.Sprintf(format, doc)).NotTo(ContainSubstring(canary))
		}
		encoded, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Contains(string(encoded), canary)).To(BeFalse())
	})
	BeforeEach(func() { keyring.MockInit(); GinkgoT().Setenv("HOME", GinkgoT().TempDir()) })
	It("prompts once and reuses the same key after reopening", func() {
		calls := 0
		opts := Options{Service: "test", Account: "kek", Confirm: func(context.Context) (bool, error) { calls++; return true, nil }}
		first, id, err := Open(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Close)
		cipher, err := first.Encrypt(context.Background(), []byte("credential"))
		Expect(err).NotTo(HaveOccurred())
		second, nextID, err := Open(context.Background(), opts)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(second.Close)
		plain, err := second.Decrypt(context.Background(), cipher)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(plain)).To(Equal("credential"))
		Expect(nextID).To(Equal(id))
		Expect(calls).To(Equal(1))
	})
	It("does not write on cancellation or without an interactive confirmation", func() {
		opts := Options{Service: "test", Account: "kek", Confirm: func(context.Context) (bool, error) { return false, nil }}
		_, _, err := Open(context.Background(), opts)
		Expect(err).To(HaveOccurred())
		_, err = keyring.Get("test", "kek")
		Expect(err).To(Equal(keyring.ErrNotFound))
		opts.Confirm = nil
		_, _, err = Open(context.Background(), opts)
		Expect(err).To(MatchError(ContainSubstring("interactively")))
	})
	It("refuses corrupt keys without prompting or replacing them", func() {
		Expect(keyring.Set("test", "kek", "broken")).To(Succeed())
		_, _, err := Open(context.Background(), Options{Service: "test", Account: "kek", Confirm: func(context.Context) (bool, error) { Fail("must not prompt"); return true, nil }})
		Expect(err).To(HaveOccurred())
		raw, err := keyring.Get("test", "kek")
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(Equal("broken"))
	})
})
