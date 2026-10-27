package crd

import (
	"fmt"
	"maps"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// FromModel is the Connection resource ToModel reads back as connection: its
// type, url, credentials, TLS flag and properties on the spec's generic
// fields, so every connection type round-trips, including those without a
// typed variant (sqlite, clickhouse, ...). A credential that is a reference
// (secret://, configmap://, helm://, serviceaccount://, op://) is written as
// its valueFrom. Runtime metadata (source, timestamps) is not part of a
// resource and is left out.
func FromModel(connection models.Connection) (*Connection, error) {
	if connection.Type == "" {
		return nil, fmt.Errorf("connection %q has no type", connection.Name)
	}
	resource := &Connection{
		TypeMeta:   metav1.TypeMeta{APIVersion: APIVersion, Kind: Kind},
		ObjectMeta: metav1.ObjectMeta{Name: connection.Name, Namespace: connection.Namespace},
		Spec: ConnectionSpec{
			Type:        connection.Type,
			InsecureTLS: connection.InsecureTLS,
			Properties:  maps.Clone(connection.Properties),
		},
	}
	if connection.ID != uuid.Nil {
		resource.UID = k8stypes.UID(connection.ID.String())
	}
	for _, field := range []struct {
		name  string
		value string
		into  *types.EnvVar
	}{
		{"url", connection.URL, &resource.Spec.URL},
		{"username", connection.Username, &resource.Spec.Username},
		{"password", connection.Password, &resource.Spec.Password},
		{"certificate", connection.Certificate, &resource.Spec.Certificate},
	} {
		if field.value == "" {
			continue
		}
		if err := field.into.Scan(field.value); err != nil {
			return nil, fmt.Errorf("connection %q %s: %w", connection.Name, field.name, err)
		}
	}
	return resource, nil
}
