// Package azuresdk is the opt-in bridge to the Azure SDK's default credential
// chain. It is the only package in commons-db that links the Azure SDK, so
// binaries that need Azure CLI / azd developer credentials import it and call
// Install; everyone else stays SDK-free.
package azuresdk

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/flanksource/commons/http/middlewares/azuread"
	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/connection"
)

// Install registers azidentity.NewDefaultAzureCredential as the last source of
// the ambient Azure credential chain used by connection.AzureConnection.
func Install() {
	connection.RegisterAzureDefaultCredential(defaultCredential)
}

func defaultCredential() (azuread.Credential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure sdk default credential: %w", err)
	}
	return Adapt(cred), nil
}

// Adapt exposes an Azure SDK token credential as an azuread.Credential.
func Adapt(cred azcore.TokenCredential) azuread.Credential {
	return adapter{cred: cred}
}

type adapter struct {
	cred azcore.TokenCredential
}

func (a adapter) Token(ctx context.Context, scope string) (*oauth2.Token, error) {
	token, err := a.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: token.Token, TokenType: "Bearer", Expiry: token.ExpiresOn}, nil
}
