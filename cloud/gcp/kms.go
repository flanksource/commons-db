package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const (
	kmsDefaultRoot = "https://cloudkms.googleapis.com"
	kmsAPIPath     = "/v1"
)

// KMSClient talks to the Cloud KMS REST API.
type KMSClient struct{ *api }

// NewKMSClient builds a Cloud KMS client.
func NewKMSClient(opts Options) (*KMSClient, error) {
	client, err := newAPI(opts, kmsDefaultRoot, kmsAPIPath)
	if err != nil {
		return nil, err
	}
	return &KMSClient{api: client}, nil
}

type kmsPayload struct {
	Plaintext  []byte `json:"plaintext,omitempty"`
	Ciphertext []byte `json:"ciphertext,omitempty"`
}

// Encrypt encrypts plaintext with the key resource name
// (projects/P/locations/L/keyRings/R/cryptoKeys/K).
func (k *KMSClient) Encrypt(ctx context.Context, keyName string, plaintext []byte) ([]byte, error) {
	out, err := k.invoke(ctx, keyName, "encrypt", kmsPayload{Plaintext: plaintext})
	if err != nil {
		return nil, err
	}
	return out.Ciphertext, nil
}

// Decrypt decrypts ciphertext produced by Encrypt with the same key.
func (k *KMSClient) Decrypt(ctx context.Context, keyName string, ciphertext []byte) ([]byte, error) {
	out, err := k.invoke(ctx, keyName, "decrypt", kmsPayload{Ciphertext: ciphertext})
	if err != nil {
		return nil, err
	}
	return out.Plaintext, nil
}

func (k *KMSClient) invoke(ctx context.Context, keyName, verb string, in kmsPayload) (*kmsPayload, error) {
	if keyName == "" || strings.HasPrefix(keyName, "/") {
		return nil, fmt.Errorf("gcp: kms %s: invalid key resource name %q", verb, keyName)
	}
	var out kmsPayload
	err := k.do(ctx, call{Method: http.MethodPost, URL: k.base + "/" + keyName + ":" + verb, Body: in, Out: &out})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
