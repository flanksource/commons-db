package connection

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/keyvault/azkeys"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"gocloud.dev/gcerrors"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/driver"
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

func (t *AzureKeyVault) SecretKeeper(ctx commonsContext.Context) (*secrets.Keeper, error) {
	creds, err := t.AzureConnection.TokenCredential()
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure token credential: %w", err)
	}

	keyVaultURI, keyName, keyVersion, err := parseAzureKeyID(t.KeyID)
	if err != nil {
		return nil, err
	}

	client, err := azkeys.NewClient(keyVaultURI, creds, &azkeys.ClientOptions{
		ClientOptions: policy.ClientOptions{},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure Key Vault client: %w", err)
	}

	return secrets.NewKeeper(&azureKeyVaultKeeper{
		client:     client,
		keyName:    keyName,
		keyVersion: keyVersion,
		algorithm:  azkeys.JSONWebKeyEncryptionAlgorithmRSAOAEP256,
	}), nil
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
	client     *azkeys.Client
	keyName    string
	keyVersion string
	algorithm  azkeys.JSONWebKeyEncryptionAlgorithm
}

var _ driver.Keeper = (*azureKeyVaultKeeper)(nil)

func (k *azureKeyVaultKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	result, err := k.client.Encrypt(ctx, k.keyName, k.keyVersion, azkeys.KeyOperationsParameters{
		Algorithm: &k.algorithm,
		Value:     plaintext,
	}, nil)
	if err != nil {
		return nil, err
	}
	return result.Result, nil
}

func (k *azureKeyVaultKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	result, err := k.client.Decrypt(ctx, k.keyName, k.keyVersion, azkeys.KeyOperationsParameters{
		Algorithm: &k.algorithm,
		Value:     ciphertext,
	}, nil)
	if err != nil {
		return nil, err
	}
	return result.Result, nil
}

func (k *azureKeyVaultKeeper) Close() error { return nil }

func (k *azureKeyVaultKeeper) ErrorAs(err error, i any) bool { return errors.As(err, i) }

func (k *azureKeyVaultKeeper) ErrorCode(err error) gcerrors.ErrorCode {
	var responseErr *azcore.ResponseError
	if !errors.As(err, &responseErr) {
		return gcerrors.Unknown
	}

	switch responseErr.StatusCode {
	case 400:
		return gcerrors.InvalidArgument
	case 401, 403:
		return gcerrors.PermissionDenied
	case 404:
		return gcerrors.NotFound
	case 409:
		return gcerrors.FailedPrecondition
	case 429:
		return gcerrors.ResourceExhausted
	case 500:
		return gcerrors.Internal
	case 503:
		return gcerrors.Unknown
	case 504:
		return gcerrors.DeadlineExceeded
	default:
		return gcerrors.Unknown
	}
}
