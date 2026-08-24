package connection

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// +kubebuilder:object:generate=true
type AzureConnection struct {
	ConnectionName string        `yaml:"connection,omitempty" json:"connection,omitempty"`
	ClientID       *types.EnvVar `yaml:"clientID,omitempty" json:"clientID,omitempty"`
	ClientSecret   *types.EnvVar `yaml:"clientSecret,omitempty" json:"clientSecret,omitempty"`
	TenantID       string        `yaml:"tenantID,omitempty" json:"tenantID,omitempty"`
}

// HydrateConnection attempts to find the connection by name
// and populate the endpoint and credentials.
func (g *AzureConnection) HydrateConnection(ctx ConnectionContext) error {
	connection, err := ctx.HydrateConnectionByURL(g.ConnectionName)
	if err != nil {
		return err
	}

	if connection != nil {
		// Each field is taken only when the row carries one. The tenant property
		// in particular is optional, and assigning it unconditionally blanked an
		// inline tenantID whenever the stored connection omitted it.
		if connection.Username != "" {
			g.ClientID = &types.EnvVar{ValueStatic: connection.Username}
		}
		if connection.Password != "" {
			g.ClientSecret = &types.EnvVar{ValueStatic: connection.Password}
		}
		if tenant := connection.Properties["tenant"]; tenant != "" {
			g.TenantID = tenant
		}
	}

	// Inline credentials arrive as a reference (secret://, a configmap key) and
	// are only literal once resolved. Without this an inline clientSecret is
	// handed to Azure verbatim as "secret://…".
	if g.ClientID != nil && !g.ClientID.IsEmpty() {
		v, err := ctx.GetEnvValueFromCache(*g.ClientID, ctx.GetNamespace())
		if err != nil {
			return fmt.Errorf("could not get clientID from env var: %w", err)
		}
		g.ClientID = &types.EnvVar{ValueStatic: v}
	}

	if g.ClientSecret != nil && !g.ClientSecret.IsEmpty() {
		v, err := ctx.GetEnvValueFromCache(*g.ClientSecret, ctx.GetNamespace())
		if err != nil {
			return fmt.Errorf("could not get clientSecret from env var: %w", err)
		}
		g.ClientSecret = &types.EnvVar{ValueStatic: v}
	}

	return nil
}

func (g *AzureConnection) FromModel(connection models.Connection) {
	g.ConnectionName = connection.Name
	g.ClientID = &types.EnvVar{ValueStatic: connection.Username}
	g.ClientSecret = &types.EnvVar{ValueStatic: connection.Password}
	if tenantID, ok := connection.Properties["tenant"]; ok {
		g.TenantID = tenantID
	}
}

func (g AzureConnection) ToModel() models.Connection {
	return models.Connection{
		Type:     models.ConnectionTypeAzure,
		Name:     g.ConnectionName,
		Username: envValue(g.ClientID),
		Password: envValue(g.ClientSecret),
		Properties: types.JSONStringMap{
			"tenant": g.TenantID,
		},
	}
}

func (g *AzureConnection) TokenCredential() (azcore.TokenCredential, error) {
	// Nothing configured at all means "use whatever identity the environment
	// provides" — env vars, workload identity, managed identity, az login — the
	// same fall-through AWSConnection.Client gets from the AWS default chain. A
	// half-filled service principal is a misconfiguration, not a request for
	// ambient auth, so it still fails below.
	if g.isUnconfigured() {
		return azidentity.NewDefaultAzureCredential(nil)
	}

	// Named explicitly rather than left to azidentity: an unset credential
	// otherwise reaches EnvVar.String() — a value receiver — and panics on the
	// nil pointer before any error can be reported.
	if g.TenantID == "" {
		return nil, fmt.Errorf("azure connection is missing tenantID")
	}
	if g.ClientID == nil || g.ClientID.IsEmpty() {
		return nil, fmt.Errorf("azure connection is missing clientID")
	}
	if g.ClientSecret == nil || g.ClientSecret.IsEmpty() {
		return nil, fmt.Errorf("azure connection is missing clientSecret")
	}

	return azidentity.NewClientSecretCredential(g.TenantID, envValue(g.ClientID), envValue(g.ClientSecret), nil)
}

// isUnconfigured reports whether the connection names no service principal at
// all, as opposed to naming an incomplete one.
func (g *AzureConnection) isUnconfigured() bool {
	return g.TenantID == "" &&
		(g.ClientID == nil || g.ClientID.IsEmpty()) &&
		(g.ClientSecret == nil || g.ClientSecret.IsEmpty())
}

// envValue reads an optional EnvVar, treating unset as empty rather than
// dereferencing nil through EnvVar's value-receiver String.
func envValue(v *types.EnvVar) string {
	if v == nil {
		return ""
	}
	return v.String()
}
