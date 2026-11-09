package connections

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/models"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ValidateOptions struct{ Reference string }
type ValidationResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (s *Service) Validate(ctx context.Context, options ValidateOptions) (ValidationResult, error) {
	raw, resolved, err := s.resolveConnection(resolveConnectionOptions{Context: ctx, ID: options.Reference, RefreshSecrets: true})
	if err != nil {
		return ValidationResult{}, err
	}
	result := ValidationResult{Name: raw.Name}
	if err := queryschema.ValidateConnection(*raw); err != nil {
		return result, err
	}
	if err := validateConnection(s.context().Wrap(ctx), resolved); err != nil {
		return result, err
	}
	if resolved.Type == models.ConnectionTypeSQLite {
		if strings.Contains(resolved.URL, ":memory:") {
			return result, fmt.Errorf("validate requires a persisted SQLite database")
		}
		u, parseErr := url.Parse(resolved.URL)
		if parseErr != nil {
			return result, parseErr
		}
		q := u.Query()
		q.Set("mode", "ro")
		u.RawQuery = q.Encode()
		resolved.URL = u.String()
	}
	switch resolved.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeSQLite,
		models.ConnectionTypeGit, models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeAzureDevops, models.ConnectionTypeS3:
		_, err = dbconnection.ProbeModel(ctx, *resolved)
		result.OK = err == nil
	default:
		info := discoverServer(ctx, s.context().Wrap(ctx), resolved)
		if info.Status == "unavailable" {
			switch resolved.Type {
			case models.ConnectionTypeHTTP, models.ConnectionTypeLoki, models.ConnectionTypeJaeger:
				probe := testConnection(s.context().Wrap(ctx), resolved)
				result.OK, result.Message = probe.OK, probe.Message
			default:
				err = fmt.Errorf("validation unsupported for connection type %q", resolved.Type)
			}
		} else {
			result.OK, result.Message = info.Status != "error", info.Message
		}
	}
	if err != nil {
		result.Message = sanitizeConnectionError(err, raw, resolved)
	}
	if !result.OK {
		result.Message = sanitizeConnectionError(fmt.Errorf("%s", result.Message), raw, resolved)
		return result, fmt.Errorf("validate connection %q: %s", raw.Name, result.Message)
	}
	result.Message = "Connection validated"
	return result, nil
}

type ConnectionInventory struct {
	*models.Connection
	Layer string `json:"layer,omitempty"`
}

func (s *Service) Inventory(options ListOptions) ([]ConnectionInventory, error) {
	listed, err := s.List(options)
	if err != nil {
		return nil, err
	}
	inventory := make([]ConnectionInventory, 0, len(listed))
	for _, c := range listed {
		inventory = append(inventory, ConnectionInventory{Connection: c, Layer: connectionLayer(c)})
	}
	return inventory, nil
}

// ResolveReference refuses ambiguous names rather than choosing an arbitrary row.
func (s *Service) ResolveReference(reference string) (*models.Connection, error) {
	if _, err := uuid.Parse(reference); err == nil {
		return s.Get(reference)
	}
	reference = strings.TrimPrefix(reference, "connection://")
	namespace, name, qualified := strings.Cut(reference, "/")
	if !qualified {
		name = namespace
	}
	all, err := s.List(ListOptions{})
	if err != nil {
		return nil, err
	}
	var found *models.Connection
	for _, c := range all {
		if c.Name != name || qualified && c.Namespace != namespace {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("connection %q is ambiguous; specify namespace/name", reference)
		}
		found = c
	}
	if found == nil {
		return nil, fmt.Errorf("connection %q not found: %w", reference, gorm.ErrRecordNotFound)
	}
	return found, nil
}
