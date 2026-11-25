package aws

import "context"

var kmsProtocol = jsonProtocol{Version: "1.1", TargetPrefix: "TrentService"}

// KMS is the Key Management Service client (JSON 1.1).
type KMS struct{ svc *service }

// KMS returns the KMS client.
func (s *Session) KMS() *KMS { return &KMS{svc: s.newService("kms", "kms", "kms")} }

// KMSEncryptInput names the key (id, ARN or alias) and the plaintext to encrypt.
type KMSEncryptInput struct {
	KeyID     string `json:"KeyId"`
	Plaintext []byte `json:"Plaintext"`
}

// KMSDecryptInput carries ciphertext produced by Encrypt; KMS finds the key from it.
type KMSDecryptInput struct {
	CiphertextBlob []byte `json:"CiphertextBlob"`
}

// Encrypt returns the ciphertext blob for the plaintext.
func (c *KMS) Encrypt(ctx context.Context, in KMSEncryptInput) ([]byte, error) {
	var out struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
	}
	if err := c.svc.jsonRPC(ctx, kmsProtocol, "Encrypt", in, &out); err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

// Decrypt returns the plaintext for a ciphertext blob.
func (c *KMS) Decrypt(ctx context.Context, in KMSDecryptInput) ([]byte, error) {
	var out struct {
		Plaintext []byte `json:"Plaintext"`
	}
	if err := c.svc.jsonRPC(ctx, kmsProtocol, "Decrypt", in, &out); err != nil {
		return nil, err
	}
	return out.Plaintext, nil
}
