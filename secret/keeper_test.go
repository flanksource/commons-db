package secret

import (
	"bytes"
	"encoding/base64"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gocloud.dev/secrets"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

var _ = Describe("KeeperFromConnection", func() {
	const (
		keySize = 32
		payload = "arbitrary credential payload"
	)

	key := func(fill byte) string {
		return base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, keySize))
	}

	It("declares and allows local_kms", func() {
		Expect(models.ConnectionTypeLocalKMS).To(Equal("local_kms"))
		Expect(allowedConnectionTypes).To(ContainElement(models.ConnectionTypeLocalKMS))
	})

	It("encrypts and decrypts with the configured key", func(ctx SpecContext) {
		connectionCtx, connectionID := keeperTestContext(models.ConnectionTypeLocalKMS, key('a'))

		keeper, err := KeeperFromConnection(connectionCtx, connectionID)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(keeper.Close)

		ciphertext, err := keeper.Encrypt(ctx, []byte(payload))
		Expect(err).NotTo(HaveOccurred())

		plaintext, err := keeper.Decrypt(ctx, ciphertext)
		Expect(err).NotTo(HaveOccurred())
		Expect(plaintext).To(Equal([]byte(payload)))
	})

	It("fails to decrypt ciphertext encrypted under a different key", func(ctx SpecContext) {
		firstCtx, firstID := keeperTestContext(models.ConnectionTypeLocalKMS, key('a'))
		secondCtx, secondID := keeperTestContext(models.ConnectionTypeLocalKMS, key('b'))

		firstKeeper, err := KeeperFromConnection(firstCtx, firstID)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(firstKeeper.Close)
		secondKeeper, err := KeeperFromConnection(secondCtx, secondID)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(secondKeeper.Close)

		ciphertext, err := firstKeeper.Encrypt(ctx, []byte(payload))
		Expect(err).NotTo(HaveOccurred())

		plaintext, err := secondKeeper.Decrypt(ctx, ciphertext)
		Expect(err).To(HaveOccurred())
		Expect(plaintext).To(BeNil())
	})

	DescribeTable("rejects invalid keys without generating a default",
		func(password, expectedError string) {
			connectionCtx, connectionID := keeperTestContext(models.ConnectionTypeLocalKMS, password)

			keeper, err := KeeperFromConnection(connectionCtx, connectionID)
			Expect(err).To(MatchError(ContainSubstring(expectedError)))
			Expect(keeper).To(BeNil())
			if password != "" {
				Expect(err.Error()).NotTo(ContainSubstring(password))
			}
		},
		Entry("missing", "", "local_kms connection key is not set"),
		Entry("malformed base64", "not-valid-base64", "invalid local_kms connection key"),
		Entry("wrong decoded length", base64.URLEncoding.EncodeToString([]byte("too short")), "want 32 bytes"),
	)

	It("still rejects connection types outside the allowlist", func() {
		connectionCtx, connectionID := keeperTestContext(models.ConnectionTypePostgres, key('a'))

		keeper, err := KeeperFromConnection(connectionCtx, connectionID)
		Expect(err).To(MatchError("connection type postgres cannot be used to create a SecretKeeper"))
		Expect(keeper).To(BeNil())
	})

	It("hands every concurrent caller the same keeper", func() {
		connectionCtx, connectionID := keeperTestContext(models.ConnectionTypeLocalKMS, key('a'))
		originalConnection := KMSConnection
		KMSConnection = connectionID
		DeferCleanup(func() {
			KMSConnection = originalConnection
			InvalidateKeeper()
		})
		InvalidateKeeper()

		const callers = 8
		keepers := make([]*secrets.Keeper, callers)
		var group errgroup.Group
		for index := range keepers {
			group.Go(func() error {
				keeper, err := createOrGetKeeper(connectionCtx)
				keepers[index] = keeper
				return err
			})
		}
		Expect(group.Wait()).To(Succeed())

		// A keeper built by a losing racer would be overwritten in the cache
		// without OnEvicted firing, so it would never be closed.
		Expect(keepers).To(HaveEach(BeIdenticalTo(keepers[0])))
	})

	It("drops the cached keeper when the KMS connection changes", func() {
		connectionCtx, connectionID := keeperTestContext(models.ConnectionTypeLocalKMS, key('a'))
		originalConnection := KMSConnection
		KMSConnection = connectionID
		DeferCleanup(func() {
			KMSConnection = originalConnection
			InvalidateKeeper()
		})
		InvalidateKeeper()

		first, err := createOrGetKeeper(connectionCtx)
		Expect(err).NotTo(HaveOccurred())
		InvalidateKeeper()
		second, err := createOrGetKeeper(connectionCtx)
		Expect(err).NotTo(HaveOccurred())

		Expect(second).NotTo(BeIdenticalTo(first))
	})

	It("does not select local_kms when no keeper connection is configured", func() {
		originalConnection := KMSConnection
		KMSConnection = ""
		DeferCleanup(func() {
			KMSConnection = originalConnection
			keeperCache.Flush()
		})
		keeperCache.Flush()

		keeper, err := createOrGetKeeper(dbcontext.New())
		Expect(err).To(MatchError("secret keeper connection is not set"))
		Expect(keeper).To(BeNil())
	})
})

func keeperTestContext(connectionType, password string) (dbcontext.Context, string) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	Expect(err).NotTo(HaveOccurred())
	Expect(db.Exec(`CREATE TABLE connections (
		id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
		url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
		insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
	)`).Error).NotTo(HaveOccurred())

	connection := models.Connection{
		ID:         uuid.New(),
		Name:       "keeper",
		Namespace:  "default",
		Type:       connectionType,
		Password:   password,
		Properties: map[string]string{},
	}
	Expect(db.Create(&connection).Error).NotTo(HaveOccurred())

	return dbcontext.New().WithDB(db, nil), connection.ID.String()
}
