// A connection policy: the connection types, and the sources of their values, a service offers
// in its form and accepts on save.
package connections

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/schema"
	"github.com/flanksource/commons-db/types"
)

// Policy narrows the connections an app offers and accepts. An empty field
// places no restriction.
type Policy struct {
	// AllowedTypes are the connection types listed, offered in the form and
	// accepted on save.
	AllowedTypes []string
	// SecretSources are where a username, password or certificate may come
	// from, and URLSources where a URL may: clicky-ui's SecretValueSource
	// vocabulary (secret, configmap, helm, serviceaccount, onepassword), plus
	// value for a literal.
	SecretSources []string
	URLSources    []string
}

// Value sources, by the reference form each is stored as: secret://,
// configmap://, helm://, serviceaccount://, op://, or a literal value.
const (
	SourceSecret         = "secret"
	SourceConfigMap      = "configmap"
	SourceHelm           = "helm"
	SourceServiceAccount = "serviceaccount"
	SourceOnePassword    = "onepassword"
	SourceValue          = "value"
)

var valueSources = []string{SourceSecret, SourceConfigMap, SourceHelm, SourceServiceAccount, SourceOnePassword, SourceValue}

func (p Policy) validate() error {
	known := schema.ConnectionTypes()
	for _, typ := range p.AllowedTypes {
		if !slices.Contains(known, typ) {
			return fmt.Errorf("connection policy: unknown connection type %q", typ)
		}
	}
	for _, source := range slices.Concat(p.SecretSources, p.URLSources) {
		if !slices.Contains(valueSources, source) {
			return fmt.Errorf("connection policy: unknown value source %q, expected one of %s", source, strings.Join(valueSources, ", "))
		}
	}
	return nil
}

func (p Policy) schemaOptions() schema.ConnectionOptions {
	return schema.ConnectionOptions{AllowedTypes: p.AllowedTypes, SecretSources: p.SecretSources, URLSources: p.URLSources}
}

// scope narrows a list's type filter to the allowed types; false means the
// list asked only for types the policy does not allow, so nothing matches.
func (p Policy) scope(options ListOptions) (ListOptions, bool) {
	if len(p.AllowedTypes) == 0 {
		return options, true
	}
	requested := connectionTypeFilter(options)
	if len(requested) == 0 {
		return ListOptions{Types: strings.Join(p.AllowedTypes, ",")}, true
	}
	allowed := slices.DeleteFunc(requested, func(typ string) bool { return !slices.Contains(p.AllowedTypes, typ) })
	return ListOptions{Types: strings.Join(allowed, ",")}, len(allowed) > 0
}

// check refuses a connection of a type the policy does not allow, and one
// whose credential or URL comes from a source it does not allow.
func (p Policy) check(connection *models.Connection) error {
	if len(p.AllowedTypes) > 0 && !slices.Contains(p.AllowedTypes, connection.Type) {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_type", "connection %q has type %q, expected one of %s",
			connection.Name, connection.Type, strings.Join(p.AllowedTypes, ", "))
	}
	for _, field := range []struct{ name, value string }{
		{"username", connection.Username}, {"password", connection.Password}, {"certificate", connection.Certificate},
	} {
		if err := checkSource(connection.Name, field.name, field.value, p.SecretSources); err != nil {
			return err
		}
	}
	for name, secret := range connection.Secrets {
		if err := checkSource(connection.Name, "secret "+name, secret.String(), p.SecretSources); err != nil {
			return err
		}
	}
	if err := checkSource(connection.Name, "url", connection.URL, p.URLSources); err != nil {
		return err
	}
	return p.checkKubernetesSelection(connection)
}

// checkKubernetesSelection refuses a connection that selects a Kubernetes
// cluster to read its values from when none of its values may come from one; a
// kubernetes connection selects the cluster it connects to.
func (p Policy) checkKubernetesSelection(connection *models.Connection) error {
	if connection.Type == models.ConnectionTypeKubernetes || p.schemaOptions().ReadsKubernetes() {
		return nil
	}
	for _, property := range []string{dbcontext.KubeconfigProperty, dbcontext.KubeContextProperty} {
		if connection.Properties[property] != "" {
			return entity.NewStatusErrorf(http.StatusBadRequest, "kubernetes_selection", "connection %q selects a Kubernetes cluster (properties.%s), but its values may come only from %s",
				connection.Name, property, strings.Join(slices.Compact(slices.Sorted(slices.Values(slices.Concat(p.SecretSources, p.URLSources)))), ", "))
		}
	}
	return nil
}

func checkSource(connection, field, value string, allowed []string) error {
	if value == "" || len(allowed) == 0 {
		return nil
	}
	source, err := valueSource(value)
	if err != nil {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_reference", "connection %q %s: %v", connection, field, err)
	}
	if !slices.Contains(allowed, source) {
		kind := "a " + source + " reference"
		if source == SourceValue {
			kind = "a literal value"
		}
		return entity.NewStatusErrorf(http.StatusBadRequest, "literal_secret", "connection %q %s is %s; it must come from %s",
			connection, field, kind, strings.Join(allowed, ", "))
	}
	return nil
}

// valueSource is the source a stored value names, in Policy's vocabulary.
func valueSource(value string) (string, error) {
	var parsed types.EnvVar
	if err := parsed.Scan(value); err != nil {
		return "", err
	}
	switch from := parsed.ValueFrom; {
	case from == nil:
		return SourceValue, nil
	case from.SecretKeyRef != nil:
		return SourceSecret, nil
	case from.ConfigMapKeyRef != nil:
		return SourceConfigMap, nil
	case from.HelmRef != nil:
		return SourceHelm, nil
	case from.ServiceAccount != nil:
		return SourceServiceAccount, nil
	case from.OnePassword != nil:
		return SourceOnePassword, nil
	}
	return "", fmt.Errorf("unrecognised value reference %q", value)
}
