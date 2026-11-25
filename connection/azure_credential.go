package connection

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/commons/http/middlewares/azuread"
	"golang.org/x/oauth2"
)

// AzureCredentialFactory builds the credential that extends the ambient Azure
// chain. It is called lazily, when a token is first requested.
type AzureCredentialFactory func() (azuread.Credential, error)

const azureSDKInstallHint = "Azure CLI/azd developer credentials require importing " +
	"github.com/flanksource/commons-db/connection/azuresdk and calling azuresdk.Install()"

var (
	azureDefaultCredentialMu sync.RWMutex
	azureDefaultCredential   AzureCredentialFactory
)

// RegisterAzureDefaultCredential installs a factory whose credential is tried
// after the SDK-free ambient chain. It is the extension point used by
// connection/azuresdk; passing nil removes a previously registered factory.
func RegisterAzureDefaultCredential(factory AzureCredentialFactory) {
	azureDefaultCredentialMu.Lock()
	defer azureDefaultCredentialMu.Unlock()
	azureDefaultCredential = factory
}

func registeredAzureCredential() AzureCredentialFactory {
	azureDefaultCredentialMu.RLock()
	defer azureDefaultCredentialMu.RUnlock()
	return azureDefaultCredential
}

// ambientAzureCredential is the chain used when a connection names no service
// principal: azuread.DefaultChain, then the registered SDK credential.
func ambientAzureCredential() azuread.Credential {
	return newAmbientCredential(azuread.DefaultChain(), registeredAzureCredential())
}

func newAmbientCredential(chain azuread.Chain, sdk AzureCredentialFactory) azuread.Credential {
	if sdk != nil {
		chain = append(chain[:len(chain):len(chain)], azuread.NamedCredential{Name: "azure sdk default credential", Credential: lazyAzureCredential(sdk)})
	}
	return ambientCredential{chain: chain, sdkInstalled: sdk != nil}
}

type ambientCredential struct {
	chain        azuread.Chain
	sdkInstalled bool
}

func (a ambientCredential) Token(ctx context.Context, scope string) (*oauth2.Token, error) {
	token, err := a.chain.Token(ctx, scope)
	if err == nil {
		return token, nil
	}
	names := make([]string, len(a.chain))
	for i, c := range a.chain {
		names[i] = c.Name
	}
	hint := ""
	if !a.sdkInstalled {
		hint = "; " + azureSDKInstallHint
	}
	return nil, fmt.Errorf("no azure credential source produced a token (tried: %s)%s: %w", strings.Join(names, ", "), hint, err)
}

type lazyAzureCredential AzureCredentialFactory

func (l lazyAzureCredential) Token(ctx context.Context, scope string) (*oauth2.Token, error) {
	cred, err := l()
	if err != nil {
		return nil, err
	}
	return cred.Token(ctx, scope)
}
