// Package keeper defines the envelope-encryption contract implemented by the
// KMS connections (AWS KMS, GCP KMS, Azure Key Vault) and the local keeper.
// It is a leaf package so that connection and secret can share it without
// linking any cloud SDK.
package keeper

import "context"

// Keeper encrypts and decrypts small payloads such as secrets and connection
// credentials. Implementations return errors that name the backend and key.
type Keeper interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	Close() error
}
