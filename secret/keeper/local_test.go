package keeper_test

import (
	"context"
	"encoding/base64"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gocloud.dev/secrets/localsecrets"

	"github.com/flanksource/commons-db/secret/keeper"
)

// gocloud's localsecrets is the format already used for stored ciphertext, so
// it is the compatibility oracle for the SDK-free local keeper.
var _ = Describe("Local keeper", func() {
	ctx := context.Background()
	rawKey := []byte("0123456789abcdef0123456789abcdef")
	encodedKey := base64.URLEncoding.EncodeToString(rawKey)
	plaintext := []byte("postgres://user:secret@db:5432/app")

	newLocal := func() keeper.Keeper {
		local, err := keeper.NewLocalFromBase64(encodedKey)
		Expect(err).NotTo(HaveOccurred())
		return local
	}
	newGocloud := func() interface {
		Encrypt(context.Context, []byte) ([]byte, error)
		Decrypt(context.Context, []byte) ([]byte, error)
	} {
		key, err := localsecrets.Base64Key(encodedKey)
		Expect(err).NotTo(HaveOccurred())
		return localsecrets.NewKeeper(key)
	}

	It("decrypts ciphertext produced by gocloud localsecrets", func() {
		ciphertext, err := newGocloud().Encrypt(ctx, plaintext)
		Expect(err).NotTo(HaveOccurred())
		Expect(newLocal().Decrypt(ctx, ciphertext)).To(Equal(plaintext))
	})

	It("produces ciphertext that gocloud localsecrets decrypts", func() {
		ciphertext, err := newLocal().Encrypt(ctx, plaintext)
		Expect(err).NotTo(HaveOccurred())
		Expect(newGocloud().Decrypt(ctx, ciphertext)).To(Equal(plaintext))
	})

	It("uses a fresh nonce for every encryption", func() {
		local := newLocal()
		first, err := local.Encrypt(ctx, plaintext)
		Expect(err).NotTo(HaveOccurred())
		second, err := local.Encrypt(ctx, plaintext)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).NotTo(Equal(second))
	})

	It("rejects tampered ciphertext", func() {
		ciphertext, err := newLocal().Encrypt(ctx, plaintext)
		Expect(err).NotTo(HaveOccurred())
		ciphertext[len(ciphertext)-1] ^= 0xff
		_, err = newLocal().Decrypt(ctx, ciphertext)
		Expect(err).To(MatchError(ContainSubstring("authentication failed")))
	})

	It("rejects ciphertext shorter than the nonce", func() {
		_, err := newLocal().Decrypt(ctx, []byte("short"))
		Expect(err).To(MatchError(ContainSubstring("shorter than the 24-byte nonce")))
	})

	DescribeTable("rejects invalid keys",
		func(key, message string) {
			_, err := keeper.NewLocalFromBase64(key)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty", "", "key is empty"),
		Entry("not base64", "%%%", "not url-safe base64"),
		Entry("wrong length", base64.URLEncoding.EncodeToString([]byte("too-short")), "want 32"),
	)
})
