package secret

import (
	"bytes"
	"encoding/base64"
	"strings"

	gocontext "context"
	dbcontext "github.com/flanksource/commons-db/context"
	commons "github.com/flanksource/commons/context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An inline local_kms:// URL is the configuration a process uses when it holds
// its own key (from an OS keyring or a key file) and has no connections table
// to look one up in. It must work without a database.
var _ = Describe("KeeperFromConnection with an inline local_kms URL", func() {
	const (
		keySize = 32
		payload = "arbitrary credential payload"
	)

	inlineKey := func(fill byte) string {
		return base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, keySize))
	}

	// A context with no DB at all: hydrating a connection row is exactly what
	// this path must not need.
	dblessContext := func() dbcontext.Context {
		return dbcontext.Context{Context: commons.NewContext(gocontext.Background())}
	}

	It("round-trips a payload without a connections row", func(ctx SpecContext) {
		keeper, err := KeeperFromConnection(dblessContext(), "local_kms://"+inlineKey('a'))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(keeper.Close)

		ciphertext, err := keeper.Encrypt(ctx, []byte(payload))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ciphertext)).NotTo(ContainSubstring(payload))

		plaintext, err := keeper.Decrypt(ctx, ciphertext)
		Expect(err).NotTo(HaveOccurred())
		Expect(plaintext).To(Equal([]byte(payload)))
	})

	It("keeps keys distinct, so one key cannot open another's ciphertext", func(ctx SpecContext) {
		first, err := KeeperFromConnection(dblessContext(), "local_kms://"+inlineKey('a'))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Close)
		second, err := KeeperFromConnection(dblessContext(), "local_kms://"+inlineKey('b'))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(second.Close)

		ciphertext, err := first.Encrypt(ctx, []byte(payload))
		Expect(err).NotTo(HaveOccurred())

		_, err = second.Decrypt(ctx, ciphertext)
		Expect(err).To(HaveOccurred())
	})

	// localsecrets mints a RANDOM key for "base64key://" with an empty host.
	// Reaching that would encrypt under a key nobody can ever reproduce, so an
	// empty key has to be refused before it gets there.
	It("refuses an empty key instead of silently generating one", func() {
		_, err := KeeperFromConnection(dblessContext(), "local_kms://")

		Expect(err).To(MatchError(ContainSubstring("key is not set")))
	})

	DescribeTable("refuses a malformed key",
		func(key string) {
			_, err := KeeperFromConnection(dblessContext(), "local_kms://"+key)

			Expect(err).To(MatchError(ContainSubstring("invalid local_kms connection key")))
		},
		Entry("not base64", "not-valid-base64-at-all"),
		Entry("wrong length", base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{'a'}, 16))),
		// Standard base64 uses + and /, which are not URL-safe; localsecrets
		// documents URLEncoding for exactly this reason.
		Entry("standard rather than url-safe base64",
			base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, keySize))),
	)

	It("does not divert any other scheme away from connection hydration", func() {
		// postgres:// has no inline-key meaning; it must still take the
		// hydration path and fail there for want of a typed connection.
		_, err := KeeperFromConnection(dblessContext(), "postgres://user:pass@localhost/db")

		Expect(err).To(HaveOccurred())
		Expect(strings.ToLower(err.Error())).NotTo(ContainSubstring("local_kms"))
	})
})
