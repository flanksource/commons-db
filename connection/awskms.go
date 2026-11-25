package connection

import (
	"context"
	"fmt"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/secret/keeper"
)

type AWSKMS struct {
	AWSConnection `json:",inline"`

	// keyID can be an alias (eg: alias/ExampleAlias?region=us-east-1) or the ARN
	KeyID string `json:"keyID,omitempty"`
}

func (t *AWSKMS) Populate(ctx ConnectionContext) error {
	return t.AWSConnection.Populate(ctx)
}

func (t *AWSKMS) FromModel(conn models.Connection) {
	t.AWSConnection.FromModel(conn)
	t.KeyID = conn.Properties["keyID"]
}

func (t *AWSKMS) SecretKeeper(ctx commonsContext.Context) (keeper.Keeper, error) {
	session, err := t.AWSConnection.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS client: %w", err)
	}

	return &awsKMSKeeper{keyID: t.KeyID, client: session.KMS()}, nil
}

type awsKMSKeeper struct {
	keyID  string
	client *cloudaws.KMS
}

var _ keeper.Keeper = (*awsKMSKeeper)(nil)

func (k *awsKMSKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := k.client.Decrypt(ctx, cloudaws.KMSDecryptInput{CiphertextBlob: ciphertext})
	if err != nil {
		return nil, fmt.Errorf("aws kms decrypt (key %s): %w", k.keyID, err)
	}
	return plaintext, nil
}

func (k *awsKMSKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	ciphertext, err := k.client.Encrypt(ctx, cloudaws.KMSEncryptInput{KeyID: k.keyID, Plaintext: plaintext})
	if err != nil {
		return nil, fmt.Errorf("aws kms encrypt (key %s): %w", k.keyID, err)
	}
	return ciphertext, nil
}

func (k *awsKMSKeeper) Close() error { return nil }
