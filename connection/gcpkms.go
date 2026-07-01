package connection

import (
	"context"
	"fmt"

	cloudkms "cloud.google.com/go/kms/apiv1"
	kmspb "cloud.google.com/go/kms/apiv1/kmspb"
	commonsContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"gocloud.dev/gcerrors"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/driver"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func (t *GCPKMS) SecretKeeper(ctx commonsContext.Context) (*secrets.Keeper, error) {
	oauthToken, err := t.GCPConnection.TokenSource(ctx, "https://www.googleapis.com/auth/cloudkms")
	if err != nil {
		return nil, fmt.Errorf("failed to create GCP oauth2 token: %w", err)
	}

	kmsClient, err := cloudkms.NewKeyManagementClient(ctx, option.WithTokenSource(oauthToken))
	if err != nil {
		return nil, fmt.Errorf("failed to create GCP KMS client: %w", err)
	}

	return secrets.NewKeeper(&gcpKMSKeeper{
		keyResourceID: t.KeyID,
		client:        kmsClient,
	}), nil
}

type gcpKMSKeeper struct {
	keyResourceID string
	client        *cloudkms.KeyManagementClient
}

var _ driver.Keeper = (*gcpKMSKeeper)(nil)

func (k *gcpKMSKeeper) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	resp, err := k.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       k.keyResourceID,
		Ciphertext: ciphertext,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetPlaintext(), nil
}

func (k *gcpKMSKeeper) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	resp, err := k.client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:      k.keyResourceID,
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetCiphertext(), nil
}

func (k *gcpKMSKeeper) Close() error { return k.client.Close() }

func (k *gcpKMSKeeper) ErrorAs(err error, i any) bool {
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	p, ok := i.(**status.Status)
	if !ok {
		return false
	}
	*p = s
	return true
}

func (k *gcpKMSKeeper) ErrorCode(err error) gcerrors.ErrorCode {
	switch status.Code(err) {
	case codes.OK:
		return gcerrors.OK
	case codes.Canceled:
		return gcerrors.Canceled
	case codes.Unknown:
		return gcerrors.Unknown
	case codes.InvalidArgument:
		return gcerrors.InvalidArgument
	case codes.DeadlineExceeded:
		return gcerrors.DeadlineExceeded
	case codes.NotFound:
		return gcerrors.NotFound
	case codes.AlreadyExists:
		return gcerrors.AlreadyExists
	case codes.PermissionDenied, codes.Unauthenticated:
		return gcerrors.PermissionDenied
	case codes.ResourceExhausted:
		return gcerrors.ResourceExhausted
	case codes.FailedPrecondition:
		return gcerrors.FailedPrecondition
	case codes.Aborted:
		return gcerrors.FailedPrecondition
	case codes.OutOfRange:
		return gcerrors.InvalidArgument
	case codes.Unimplemented:
		return gcerrors.Unimplemented
	case codes.Internal, codes.DataLoss:
		return gcerrors.Internal
	case codes.Unavailable:
		return gcerrors.Unknown
	default:
		return gcerrors.Unknown
	}
}
