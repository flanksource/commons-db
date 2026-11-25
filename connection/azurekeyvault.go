package connection

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/commons-db/cloud/azure"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/secret/keeper"
)

type AzureKeyVault struct {
	AzureConnection `json:",inline"`

	// keyID is a URL to the key in the format
	// 	https://<vault-name>.vault.azure.net/keys/<key-name>
	KeyID string `json:"keyID,omitempty"`
}

func (t *AzureKeyVault) Populate(ctx ConnectionContext) error {
	return t.AzureConnection.HydrateConnection(ctx)
}

func (t *AzureKeyVault) FromModel(conn models.Connection) {
	t.AzureConnection.FromModel(conn)
	t.KeyID = conn.Properties["keyID"]
}

func (t *AzureKeyVault) SecretKeeper(ctx commonsContext.Context) (keeper.Keeper, error) {
	creds, err := t.AzureConnection.Credential(AzureCredentialOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}

	keyVaultURI, keyName, keyVersion, err := parseAzureKeyID(t.KeyID)
	if err != nil {
		return nil, err
	}

	client, err := azure.NewKeyVaultClient(azure.KeyVaultOptions{VaultURL: keyVaultURI, Credential: creds})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure Key Vault client: %w", err)
	}

	return &azureKeyVaultKeeper{
		client:     client,
		keyName:    keyName,
		keyVersion: keyVersion,
		algorithm:  azure.AlgRSAOAEP256,
	}, nil
}

var azureKeyIDRE = regexp.MustCompile(`^(https://.+\.vault\.(?:[a-z\d-.]+)/)keys/(.+)$`)

func parseAzureKeyID(keyID string) (keyVaultURI, keyName, keyVersion string, err error) {
	matches := azureKeyIDRE.FindStringSubmatch(keyID)
	if len(matches) != 3 {
		return "", "", "", fmt.Errorf("invalid keyID %q", keyID)
	}

	parts := strings.SplitN(matches[2], "/", 2)
	keyName = parts[0]
	if len(parts) > 1 {
		keyVersion = parts[1]
	}
	return matches[1], keyName, keyVersion, nil
}

type azureKeyVaultKeeper struct {
	client     *azure.KeyVaultClient
	keyName    string
	keyVersion string
	algorithm  string
}

var _ keeper.Keeper = (*azureKeyVaultKeeper)(nil)

func (k *azureKeyVaultKeeper) operation(value []byte) azure.KeyOperation {
	return azure.KeyOperation{Name: k.keyName, Version: k.keyVersion, Algorithm: k.algorithm, Value: value}
}

func (k *azureKeyVaultKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	result, err := k.client.Encrypt(ctx, k.operation(plaintext))
	if err != nil {
		return nil, fmt.Errorf("azure key vault encrypt (key %s): %w", k.keyName, err)
	}
	return result, nil
}

func (k *azureKeyVaultKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	result, err := k.client.Decrypt(ctx, k.operation(ciphertext))
	if err != nil {
		return nil, fmt.Errorf("azure key vault decrypt (key %s): %w", k.keyName, err)
	}
	return result, nil
}

func (k *azureKeyVaultKeeper) Close() error { return nil }
