package drivers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// CloudSQLOptions are the Cloud SQL settings a DSN asks for.
type CloudSQLOptions struct {
	User, Database, InstanceConnectionName string
	UsePrivateIP                           bool
}

// CloudSQLDialer builds the pgx config that dials a Cloud SQL instance.
type CloudSQLDialer func(ctx context.Context, options CloudSQLOptions) (*pgx.ConnConfig, error)

var (
	dialerMu       sync.RWMutex
	cloudSQLDialer CloudSQLDialer
)

// RegisterDialer wires in the Cloud SQL dialer; it is called by
// drivers/cloudsql.Install so this package never links the Cloud SQL SDK.
func RegisterDialer(dialer CloudSQLDialer) {
	dialerMu.Lock()
	defer dialerMu.Unlock()
	cloudSQLDialer = dialer
}

func registeredDialer() CloudSQLDialer {
	dialerMu.RLock()
	defer dialerMu.RUnlock()
	return cloudSQLDialer
}

func ParseURL(connection string) (*pgx.ConnConfig, error) {
	if !strings.Contains(connection, "cloudsql-instance-connection-name") {
		return nil, nil
	}

	dialer := registeredDialer()
	if dialer == nil {
		return nil, fmt.Errorf("connection asks for cloudsql-instance-connection-name but no Cloud SQL dialer is registered: import github.com/flanksource/commons-db/drivers/cloudsql and call cloudsql.Install()")
	}

	parsed := parseParams(connection)
	usePrivateIP, _ := strconv.ParseBool(parsed["use-private-ip"])
	return dialer(context.TODO(), CloudSQLOptions{
		User:                   parsed["user"],
		Database:               parsed["db"],
		InstanceConnectionName: parsed["cloudsql-instance-connection-name"],
		UsePrivateIP:           usePrivateIP,
	})
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
