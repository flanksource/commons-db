package connection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons/http/middlewares/azuread"
	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/types"
)

type stubCredential struct {
	token string
	err   error
}

func (s stubCredential) Token(context.Context, string) (*oauth2.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &oauth2.Token{AccessToken: s.token, Expiry: time.Now().Add(time.Hour)}, nil
}

func failingChain() azuread.Chain {
	return azuread.Chain{
		{Name: "source-a", Credential: stubCredential{err: errors.New("a unavailable")}},
		{Name: "source-b", Credential: stubCredential{err: errors.New("b unavailable")}},
	}
}

func TestAzureCredentialUsesConnectionClientSecret(t *testing.T) {
	conn := AzureConnection{
		TenantID:     "tenant",
		ClientID:     &types.EnvVar{ValueStatic: "client"},
		ClientSecret: &types.EnvVar{ValueStatic: "secret"},
	}
	cred, err := conn.Credential(AzureCredentialOptions{Authority: "https://login.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	want := azuread.ClientSecret{TenantID: "tenant", ClientID: "client", ClientSecret: "secret", Authority: "https://login.example.test"}
	if cred != azuread.Credential(want) {
		t.Fatalf("credential = %#v, want %#v", cred, want)
	}
}

func TestAzureCredentialAuthorityRequiresClientSecret(t *testing.T) {
	_, err := (&AzureConnection{}).Credential(AzureCredentialOptions{Authority: "https://login.example.test"})
	if err == nil || !strings.Contains(err.Error(), "requires a client secret") {
		t.Fatalf("err = %v, want an authority/client-secret error", err)
	}
}

func TestAmbientCredentialNamesSourcesAndAzureSDKHint(t *testing.T) {
	_, err := newAmbientCredential(failingChain(), nil).Token(context.Background(), "scope")
	if err == nil {
		t.Fatal("expected an error when no source resolves")
	}
	for _, want := range []string{"source-a", "source-b", "a unavailable", "connection/azuresdk", "azuresdk.Install()"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestAmbientCredentialFallsThroughToRegisteredSDKCredential(t *testing.T) {
	sdk := func() (azuread.Credential, error) { return stubCredential{token: "from-sdk"}, nil }
	token, err := newAmbientCredential(failingChain(), sdk).Token(context.Background(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "from-sdk" {
		t.Fatalf("token = %q, want the SDK credential's token", token.AccessToken)
	}
}

func TestAmbientCredentialWithSDKFailureOmitsInstallHint(t *testing.T) {
	sdk := func() (azuread.Credential, error) { return nil, errors.New("sdk build failed") }
	_, err := newAmbientCredential(failingChain(), sdk).Token(context.Background(), "scope")
	if err == nil || strings.Contains(err.Error(), "azuresdk.Install()") || !strings.Contains(err.Error(), "sdk build failed") {
		t.Fatalf("err = %v, want the SDK failure without the install hint", err)
	}
}

func TestAmbientAzureCredentialAppendsRegisteredFactoryToDefaultChain(t *testing.T) {
	for _, name := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_FEDERATED_TOKEN_FILE"} {
		t.Setenv(name, "")
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no identity"}`, http.StatusBadRequest)
	}))
	defer failing.Close()
	t.Setenv("IDENTITY_ENDPOINT", failing.URL)
	t.Setenv("IDENTITY_HEADER", "header")

	RegisterAzureDefaultCredential(func() (azuread.Credential, error) { return stubCredential{token: "hooked"}, nil })
	t.Cleanup(func() { RegisterAzureDefaultCredential(nil) })

	token, err := ambientAzureCredential().Token(context.Background(), "https://vault.azure.net/.default")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "hooked" {
		t.Fatalf("token = %q, want the registered credential's token", token.AccessToken)
	}
}
