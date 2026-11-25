package azuresdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/flanksource/commons-db/connection/azuresdk"
)

type fakeSDKCredential struct {
	scopes []string
	expiry time.Time
}

func (f *fakeSDKCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scopes = opts.Scopes
	return azcore.AccessToken{Token: "sdk-token", ExpiresOn: f.expiry}, nil
}

func TestAdaptPassesScopeAndMapsToken(t *testing.T) {
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	fake := &fakeSDKCredential{expiry: expiry}

	token, err := azuresdk.Adapt(fake).Token(context.Background(), "https://vault.azure.net/.default")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "sdk-token" || !token.Expiry.Equal(expiry) {
		t.Fatalf("token = %+v, want sdk-token expiring %v", token, expiry)
	}
	if len(fake.scopes) != 1 || fake.scopes[0] != "https://vault.azure.net/.default" {
		t.Fatalf("scopes = %v", fake.scopes)
	}
}
