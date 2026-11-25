package connection

import (
	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
	"github.com/flanksource/commons-db/types"
)

// +kubebuilder:object:generate=true
type GCSConnection struct {
	GCPConnection `json:",inline"`
	Bucket        string `yaml:"bucket,omitempty" json:"bucket,omitempty"`
}

func (g *GCSConnection) Validate() *GCSConnection {
	if g == nil {
		return &GCSConnection{}
	}

	return g
}

// Client builds the Cloud Storage client for the connection. A connection with
// no credentials is unauthenticated (an emulator or a public bucket).
func (g *GCSConnection) Client(ctx context.Context) (*gcp.StorageClient, error) {
	g = g.Validate()

	credential := ""
	if g.Credentials != nil && !g.Credentials.IsEmpty() {
		var err error
		if credential, err = ctx.GetEnvValueFromCache(*g.Credentials, ctx.GetNamespace()); err != nil {
			return nil, err
		}
	}

	opts, err := g.ClientOptions(ctx, credential, gcp.ScopeDevStorageFull)
	if err != nil {
		return nil, err
	}
	return gcp.NewStorageClient(opts)
}

// HydrateConnection attempts to find the connection by name
// and populate the endpoint and credentials.
func (g *GCSConnection) HydrateConnection(ctx ConnectionContext) error {
	connection, err := ctx.HydrateConnectionByURL(g.ConnectionName)
	if err != nil {
		return err
	}

	if connection != nil {
		g.Credentials = &types.EnvVar{ValueStatic: connection.Certificate}
		g.Endpoint = connection.URL
		if val, ok := connection.Properties["bucket"]; ok {
			g.Bucket = val
		}
	}

	return nil
}

func (t *GCSConnection) GetProperties() map[string]string {
	return map[string]string{
		"bucket": t.Bucket,
	}
}

// Filesystem returns a filesystem interface for GCS operations
func (c *GCSConnection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
	if err := c.HydrateConnection(ctx); err != nil {
		return nil, err
	}
	client, err := c.Client(ctx)
	if err != nil {
		return nil, err
	}
	return fs.NewGCSFS(ctx, c.Bucket, client), nil
}
