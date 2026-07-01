package connection

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"gocloud.dev/gcerrors"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/driver"
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

func (t *AWSKMS) SecretKeeper(ctx commonsContext.Context) (*secrets.Keeper, error) {
	awsConfig, err := t.AWSConnection.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS client: %w", err)
	}

	keeper := secrets.NewKeeper(&awsKMSKeeper{
		keyID:  t.KeyID,
		client: kms.NewFromConfig(awsConfig),
	})
	return keeper, nil
}

type awsKMSKeeper struct {
	keyID  string
	client *kms.Client
}

var _ driver.Keeper = (*awsKMSKeeper)(nil)

func (k *awsKMSKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	result, err := k.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, err
	}
	return result.Plaintext, nil
}

func (k *awsKMSKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	result, err := k.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(k.keyID),
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, err
	}
	return result.CiphertextBlob, nil
}

func (k *awsKMSKeeper) Close() error { return nil }

func (k *awsKMSKeeper) ErrorAs(err error, i any) bool {
	return errors.As(err, i)
}

func (k *awsKMSKeeper) ErrorCode(err error) gcerrors.ErrorCode {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return gcerrors.Unknown
	}

	switch apiErr.ErrorCode() {
	case (&types.NotFoundException{}).ErrorCode():
		return gcerrors.NotFound
	case (&types.InvalidCiphertextException{}).ErrorCode(), (&types.InvalidKeyUsageException{}).ErrorCode():
		return gcerrors.InvalidArgument
	case (&types.KMSInternalException{}).ErrorCode():
		return gcerrors.Internal
	case (&types.KMSInvalidStateException{}).ErrorCode():
		return gcerrors.FailedPrecondition
	case (&types.DisabledException{}).ErrorCode(), (&types.InvalidGrantTokenException{}).ErrorCode():
		return gcerrors.PermissionDenied
	case (&types.KeyUnavailableException{}).ErrorCode():
		return gcerrors.ResourceExhausted
	case (&types.DependencyTimeoutException{}).ErrorCode():
		return gcerrors.DeadlineExceeded
	default:
		return gcerrors.Unknown
	}
}
