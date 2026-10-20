package connections

import (
	"context"
	"net/http"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/models"
)

// WritableConnectionProvider persists host-owned connections outside the service's database.
// SaveConnection receives a complete connection, including preserved credentials;
// a zero ID creates an entry and a non-zero ID updates the existing entry.
type WritableConnectionProvider interface {
	VirtualConnectionProvider
	Writable() bool
	SaveConnection(context.Context, *models.Connection) (*models.Connection, error)
	DeleteConnection(context.Context, string) error
}

func providerWritable(provider VirtualConnectionProvider) bool {
	writer, ok := provider.(WritableConnectionProvider)
	return ok && writer.Writable()
}

func writableProvider(provider VirtualConnectionProvider) (WritableConnectionProvider, error) {
	writer, ok := provider.(WritableConnectionProvider)
	if !ok || !writer.Writable() {
		return nil, entity.NewStatusError(http.StatusMethodNotAllowed, "read_only", "connection catalog is read-only")
	}
	return writer, nil
}

func (s *Service) saveProvided(ctx context.Context, id string, body map[string]any) (*models.Connection, error) {
	writer, err := writableProvider(s.virtual)
	if err != nil {
		return nil, err
	}
	body, err = s.decodeBody(ctx, body)
	if err != nil {
		return nil, err
	}
	candidate, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	if id != "" {
		existing, err := s.findConnection(id)
		if err != nil {
			return nil, err
		}
		existing = cloneConnection(existing)
		applyConnectionUpdate(existing, candidate)
		candidate = existing
	}
	resolver := s.context().Wrap(ctx).WithConnectionResolver(s.virtual.ResolveConnection)
	if err := validateConnection(resolver, candidate); err != nil {
		return nil, err
	}
	saved, err := writer.SaveConnection(ctx, candidate)
	if err != nil {
		return nil, err
	}
	forgetConnectionHealth(saved.ID.String())
	saved = cloneConnection(saved)
	redactConnection(saved)
	return saved, nil
}
