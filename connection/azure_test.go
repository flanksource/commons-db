package connection

import (
	"strings"
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

func TestAzureTokenCredentialFallsBackToAmbientIdentity(t *testing.T) {
	credential, err := (&AzureConnection{}).TokenCredential()
	if err != nil {
		t.Fatalf("unconfigured azure connection should use the ambient identity chain: %v", err)
	}
	if credential == nil {
		t.Fatal("expected an ambient credential")
	}
}

func TestAzureTokenCredentialRejectsPartialServicePrincipal(t *testing.T) {
	tests := []struct {
		name    string
		conn    AzureConnection
		missing string
	}{
		{
			name:    "client id without tenant",
			conn:    AzureConnection{ClientID: &types.EnvVar{ValueStatic: "client"}},
			missing: "tenantID",
		},
		{
			name:    "tenant without client id",
			conn:    AzureConnection{TenantID: "tenant"},
			missing: "clientID",
		},
		{
			name:    "tenant and client id without secret",
			conn:    AzureConnection{TenantID: "tenant", ClientID: &types.EnvVar{ValueStatic: "client"}},
			missing: "clientSecret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.conn.TokenCredential()
			if err == nil {
				t.Fatal("expected a partially configured service principal to fail rather than silently fall back")
			}
			if !strings.Contains(err.Error(), tt.missing) {
				t.Fatalf("error %q does not name the missing field %q", err, tt.missing)
			}
		})
	}
}

func TestAzureHydrateConnectionKeepsInlineValuesTheRowDoesNotCarry(t *testing.T) {
	conn := AzureConnection{
		ConnectionName: "connection://azure",
		ClientID:       &types.EnvVar{ValueStatic: "inline-client"},
		ClientSecret:   &types.EnvVar{ValueStatic: "inline-secret"},
		TenantID:       "inline-tenant",
	}

	// A row carrying no credentials of its own — the tenant property in
	// particular is optional, and used to blow away the inline value.
	ctx := newStubContext(&models.Connection{Name: "azure", Type: models.ConnectionTypeAzure})
	if err := conn.HydrateConnection(ctx); err != nil {
		t.Fatal(err)
	}

	if conn.TenantID != "inline-tenant" {
		t.Fatalf("tenantID = %q, want the inline value preserved", conn.TenantID)
	}
	if envValue(conn.ClientID) != "inline-client" || envValue(conn.ClientSecret) != "inline-secret" {
		t.Fatalf("credentials = %q/%q, want the inline values preserved", envValue(conn.ClientID), envValue(conn.ClientSecret))
	}
}

func TestAzureHydrateConnectionPrefersTheStoredRow(t *testing.T) {
	conn := AzureConnection{ConnectionName: "connection://azure"}
	ctx := newStubContext(&models.Connection{
		Name: "azure", Type: models.ConnectionTypeAzure,
		Username: "row-client", Password: "row-secret",
		Properties: types.JSONStringMap{"tenant": "row-tenant"},
	})
	if err := conn.HydrateConnection(ctx); err != nil {
		t.Fatal(err)
	}

	if conn.TenantID != "row-tenant" || envValue(conn.ClientID) != "row-client" || envValue(conn.ClientSecret) != "row-secret" {
		t.Fatalf("hydrated connection = %q/%q/%q", conn.TenantID, envValue(conn.ClientID), envValue(conn.ClientSecret))
	}
}
