package connection

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/cloud/gcp"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/secret/keeper"
)

type GCPKMS struct {
	GCPConnection `json:",inline"`

	// keyID points to the key in the format
	// projects/MYPROJECT/locations/MYLOCATION/keyRings/MYKEYRING/cryptoKeys/MYKEY
	KeyID string `json:"keyID,omitempty"`
}

func (t *GCPKMS) Populate(ctx ConnectionContext) error {
	return t.GCPConnection.HydrateConnection(ctx)
}

func (t *GCPKMS) FromModel(conn models.Connection) {
	t.GCPConnection.FromModel(conn)
	t.KeyID = conn.Properties["keyID"]
}

func (t *GCPKMS) SecretKeeper(ctx commonsContext.Context) (keeper.Keeper, error) {
	tokenSource, err := t.GCPConnection.TokenSource(ctx, gcp.ScopeKMS)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCP oauth2 token: %w", err)
	}

	kmsClient, err := gcp.NewKMSClient(gcp.Options{TokenSource: tokenSource})
	if err != nil {
		return nil, fmt.Errorf("failed to create GCP KMS client: %w", err)
	}

	return &gcpKMSKeeper{
		keyResourceID: t.KeyID,
		client:        kmsClient,
	}, nil
}

type gcpKMSKeeper struct {
	keyResourceID string
	client        *gcp.KMSClient
}

var _ keeper.Keeper = (*gcpKMSKeeper)(nil)

func (k *gcpKMSKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := k.client.Decrypt(ctx, k.keyResourceID, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("gcp kms decrypt (key %s): %w", k.keyResourceID, err)
	}
	return plaintext, nil
}

func (k *gcpKMSKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	ciphertext, err := k.client.Encrypt(ctx, k.keyResourceID, plaintext)
	if err != nil {
		return nil, fmt.Errorf("gcp kms encrypt (key %s): %w", k.keyResourceID, err)
	}
	return ciphertext, nil
}

func (k *gcpKMSKeeper) Close() error { return nil }
