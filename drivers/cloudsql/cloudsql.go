// Package cloudsql supplies the Cloud SQL dialer for drivers.ParseURL. It is a
// separate package so importing drivers does not link the Cloud SQL dialer. The
// dialer is a port of the ephemeral-certificate flow of cloud.google.com/go/cloudsqlconn
// (see dialer.go) over cloud/gcp's Cloud SQL Admin client.
package cloudsql

import (
	"context"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/drivers"
)

// Install registers the Cloud SQL dialer with drivers.
func Install() {
	drivers.RegisterDialer(Dial)
}

// Dial builds a pgx config whose DialFunc connects through the Cloud SQL
// ephemeral-certificate flow with IAM database authentication, using the
// ambient application default credentials.
func Dial(ctx context.Context, options drivers.CloudSQLOptions) (*pgx.ConnConfig, error) {
	cloudDialer, err := newDefaultDialer(ctx)
	if err != nil {
		return nil, err
	}
	return pgxConfig(cloudDialer, options)
}

func newDefaultDialer(ctx context.Context) (*dialer, error) {
	adminTokens, err := gcp.DefaultTokenSource(ctx, gcp.ScopeSQLAdmin)
	if err != nil {
		return nil, fmt.Errorf("cloudsql: creating default credentials: %w", err)
	}
	loginTokens, err := gcp.DefaultTokenSource(ctx, gcp.ScopeSQLLogin)
	if err != nil {
		return nil, fmt.Errorf("cloudsql: creating default login credentials: %w", err)
	}
	admin, err := gcp.NewSQLAdminClient(gcp.Options{TokenSource: adminTokens})
	if err != nil {
		return nil, fmt.Errorf("cloudsql: creating sqladmin client: %w", err)
	}
	return newDialer(dialerConfig{Admin: admin, LoginTokens: loginTokens}), nil
}

func pgxConfig(cloudDialer *dialer, options drivers.CloudSQLOptions) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(fmt.Sprintf("user=%s database=%s", options.User, options.Database))
	if err != nil {
		return nil, err
	}

	dial := dialOptions{InstanceConnectionName: options.InstanceConnectionName, PrivateIP: options.UsePrivateIP}
	config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		return cloudDialer.Dial(ctx, dial)
	}
	return config, nil
}
