package report

import (
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

const (
	// PropertyURL names the facet server used when a report does not name one.
	PropertyURL = "facet.url"
	// PropertyConnection names the facet connection used when neither the
	// report nor facet.url names a server.
	PropertyConnection = "facet.connection"
)

// Options select the facet server for one render.
type Options struct {
	Connection   string
	URL          string
	Timeout      time.Duration
	TimestampURL string
}

// ResolveServer picks the facet render service, preferring what the report asks
// for and falling back to the facet.url and facet.connection properties. A zero
// Server means none is configured and the local binary is used.
func ResolveServer(ctx dbcontext.Context, options Options) (Server, error) {
	server, err := resolveServer(ctx, options)
	if err != nil {
		return Server{}, err
	}
	server.Timeout = options.Timeout
	if server.TimestampURL == "" {
		server.TimestampURL = options.TimestampURL
	}
	return server, nil
}

func resolveServer(ctx dbcontext.Context, options Options) (Server, error) {
	if options.URL != "" || options.Connection != "" {
		server := Server{TimestampURL: options.TimestampURL}
		if options.Connection != "" {
			resolved, err := resolveConnection(ctx, options.Connection)
			if err != nil {
				return Server{}, err
			}
			server = resolved
		}
		// An explicit URL wins over the connection's, so a connection can carry
		// only the credential.
		if options.URL != "" {
			server.BaseURL = options.URL
		}
		return server, nil
	}

	if url := ctx.Properties().String(PropertyURL, ""); url != "" {
		return Server{BaseURL: url, TimestampURL: options.TimestampURL}, nil
	}
	if name := ctx.Properties().String(PropertyConnection, ""); name != "" {
		return resolveConnection(ctx, name)
	}
	return Server{TimestampURL: options.TimestampURL}, nil
}

func resolveConnection(ctx dbcontext.Context, name string) (Server, error) {
	namespace := ""
	if parts := strings.SplitN(name, "/", 2); len(parts) == 2 {
		namespace, name = parts[0], parts[1]
	}

	connection, err := ctx.GetConnection(name, namespace)
	if err != nil {
		return Server{}, fmt.Errorf("facet connection %q: %w", name, err)
	}
	if connection == nil {
		return Server{}, fmt.Errorf("facet connection %q not found", name)
	}
	if connection.Type != models.ConnectionTypeFacet {
		return Server{}, fmt.Errorf("connection %q is type %q, expected %q",
			name, connection.Type, models.ConnectionTypeFacet)
	}

	return Server{
		BaseURL:      connection.URL,
		Token:        connection.Password,
		TimestampURL: connection.Properties["timestampUrl"],
	}, nil
}
