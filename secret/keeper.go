package secret

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"github.com/samber/oops"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"
)

const defaultKeeperTTL = time.Minute * 10

var (
	keeperCache = cache.New(defaultKeeperTTL, defaultKeeperTTL*2)

	// keeperLock locks access to the keeperCache
	keeperLock sync.RWMutex
)

var (
	// KMSConnection is the connection to the key management service
	// that's used to encrypt and decrypt secrets.
	KMSConnection string

	allowedConnectionTypes = []string{
		models.ConnectionTypeAWSKMS,
		models.ConnectionTypeGCPKMS,
		models.ConnectionTypeAzureKeyVault,
		models.ConnectionTypeLocalKMS,
		// Vault not supported yet
	}
)

func init() {
	keeperCache.OnEvicted(func(key string, keeper interface{}) {
		if keeper != nil {
			keeper.(*secrets.Keeper).Close()
		}
	})
}

// createOrGetKeeper creates a new Keeper from the KMSConnection if it doesn't
// exist in the cache, otherwise it returns the cached Keeper.
func createOrGetKeeper(ctx context.Context) (*secrets.Keeper, error) {
	if KMSConnection == "" {
		return nil, oops.Errorf("secret keeper connection is not set")
	}

	keeperLock.RLock()
	cached, ok := keeperCache.Get("keeper")
	keeperLock.RUnlock()
	if ok {
		return cached.(*secrets.Keeper), nil
	}

	keeperLock.Lock()
	defer keeperLock.Unlock()

	// Re-check under the write lock: without it two concurrent misses each build
	// a keeper and the second Set silently overwrites the first. go-cache does
	// not fire OnEvicted on overwrite, so the orphan would never be closed.
	if cached, ok := keeperCache.Get("keeper"); ok {
		return cached.(*secrets.Keeper), nil
	}

	keeper, err := KeeperFromConnection(ctx, KMSConnection)
	if err != nil {
		return nil, err
	}

	ttl := ctx.Properties().Duration("secretkeeper.cache.ttl", defaultKeeperTTL)
	keeperCache.Set("keeper", keeper, ttl)
	return keeper, nil
}

// InvalidateKeeper drops the cached keeper so a KMSConnection change takes
// effect immediately instead of after the cache TTL.
func InvalidateKeeper() {
	keeperLock.Lock()
	defer keeperLock.Unlock()
	keeperCache.Delete("keeper")
}

// localKMSInlinePrefix is the inline form of a local_kms connection:
// local_kms://<url-safe base64 32-byte key>.
//
// It exists because a process that already holds its own key — from an OS
// keyring or a key file — has nowhere to look a connections row up. Resolving
// it through HydrateConnectionByURL cannot work: models.ConnectionFromURL sets
// no Type, and teaching it to derive one from the scheme would change the type
// of every other hydrated URL, which several consumers branch and reject on.
const localKMSInlinePrefix = models.ConnectionTypeLocalKMS + "://"

func KeeperFromConnection(ctx context.Context, connectionString string) (*secrets.Keeper, error) {
	if key, ok := strings.CutPrefix(strings.TrimSpace(connectionString), localKMSInlinePrefix); ok {
		return openLocalKMSKeeper(ctx, key)
	}

	conn, err := ctx.HydrateConnectionByURL(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate connection: %w", err)
	} else if conn == nil {
		return nil, fmt.Errorf("connection not found: %s", connectionString)
	}

	if !lo.Contains(allowedConnectionTypes, conn.Type) {
		return nil, fmt.Errorf("connection type %s cannot be used to create a SecretKeeper", conn.Type)
	}

	switch conn.Type {
	case models.ConnectionTypeAWSKMS:
		var kmsConn connection.AWSKMS
		kmsConn.FromModel(*conn)
		return kmsConn.SecretKeeper(ctx)

	case models.ConnectionTypeAzureKeyVault:
		var keyvaultConn connection.AzureKeyVault
		keyvaultConn.FromModel(*conn)
		return keyvaultConn.SecretKeeper(ctx)

	case models.ConnectionTypeGCPKMS:
		var kmsConn connection.GCPKMS
		kmsConn.FromModel(*conn)
		return kmsConn.SecretKeeper(ctx)

	case models.ConnectionTypeLocalKMS:
		return openLocalKMSKeeper(ctx, conn.Password)
	}

	return nil, nil
}

// openLocalKMSKeeper builds a localsecrets keeper from a url-safe base64 key,
// shared by the inline URL form and a connections row's password.
func openLocalKMSKeeper(ctx context.Context, key string) (*secrets.Keeper, error) {
	if key == "" {
		// localsecrets mints a fresh random key for "base64key://" with an
		// empty host. Reaching that would encrypt under a key nobody can ever
		// reproduce, so refuse before it can happen.
		return nil, fmt.Errorf("local_kms connection key is not set")
	}
	if _, err := localsecrets.Base64Key(key); err != nil {
		return nil, fmt.Errorf("invalid local_kms connection key: %w", err)
	}
	return secrets.OpenKeeper(ctx, fmt.Sprintf("%s://%s", localsecrets.Scheme, key))
}
