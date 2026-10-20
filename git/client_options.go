package git

import commonshttp "github.com/flanksource/commons/http"

// ClientOptions shares transport connections while keeping middleware per client.
type ClientOptions struct {
	TransportPool     *commonshttp.TransportPool
	SecurityNamespace string
}
