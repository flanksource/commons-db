package drivers

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5"
)

func ParseURL(connection string) (*pgx.ConnConfig, error) {
	if strings.Contains(connection, "cloudsql-instance-connection-name") {
		parsed := parseParams(connection)
		userPrivateIP, _ := strconv.ParseBool(parsed["use-private-ip"])
		return setupCloudSQL(context.TODO(), cloudSQLOptions{
			User:                   parsed["user"],
			Database:               parsed["db"],
			InstanceConnectionName: parsed["cloudsql-instance-connection-name"],
			UsePrivateIP:           userPrivateIP,
		})
	}

	return nil, nil
}

type cloudSQLOptions struct {
	User, Database, InstanceConnectionName string
	UsePrivateIP                           bool
}

func setupCloudSQL(ctx context.Context, cloudSQL cloudSQLOptions) (*pgx.ConnConfig, error) {
	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())
	if err != nil {
		return nil, fmt.Errorf("cloudsqlconn.NewDialer: %w", err)
	}

	var opts []cloudsqlconn.DialOption
	if cloudSQL.UsePrivateIP {
		opts = append(opts, cloudsqlconn.WithPrivateIP())
	}

	dsn := fmt.Sprintf("user=%s database=%s", cloudSQL.User, cloudSQL.Database)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}

	config.DialFunc = func(ctx context.Context, network, instance string) (net.Conn, error) {
		return dialer.Dial(ctx, cloudSQL.InstanceConnectionName, opts...)
	}
	return config, nil
}

// parseParams takes a string of key-value pairs separated by spaces and returns a map of parsed parameters.
func parseParams(input string) map[string]string {
	params := make(map[string]string)

	pairs := strings.Fields(input)
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			params[parts[0]] = parts[1]
		}
	}

	return params
}
