package connections

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/models"
)

func (s *Service) moveConnection(ctx context.Context, previous *models.Connection, body map[string]any) (*models.Connection, error) {
	if previous.ReadOnly || previous.Virtual {
		return nil, fmt.Errorf("connection %q is read-only", previous.Name)
	}
	candidate, err := connectionFromBody(body)
	if err != nil {
		return nil, err
	}
	if err := s.policy.check(candidate); err != nil {
		return nil, err
	}
	if err := validateConnection(s.context().Wrap(ctx), candidate); err != nil {
		return nil, err
	}
	if connectionLayer(previous) != "db" && candidate.Layer != "db" {
		candidate.ID = previous.ID
		saved, err := s.saveLayer(ctx, candidate)
		if err != nil {
			return nil, err
		}
		redactConnection(saved)
		return saved, nil
	}
	candidate.ID = previous.ID
	var saved *models.Connection
	if candidate.Layer == "db" {
		candidate.Source = ""
		db, err := s.store()
		if err != nil {
			return nil, err
		}
		if err := db.Create(candidate).Error; err != nil {
			return nil, err
		}
		saved = candidate
	} else {
		saved, err = s.saveLayer(ctx, candidate)
		if err != nil {
			return nil, err
		}
	}
	if err := s.deleteLayer(ctx, previous); err != nil {
		return nil, errors.Join(err, s.deleteLayer(ctx, saved))
	}
	redactConnection(saved)
	return saved, nil
}

func (s *Service) deleteLayer(ctx context.Context, c *models.Connection) error {
	if connectionLayer(c) == "db" {
		db, err := s.store()
		if err != nil {
			return err
		}
		return classifyReferenceConflict(deleteConnection(db, c.ID.String()), "move", c.Name)
	}
	writer, err := writableProvider(s.virtual)
	if err != nil {
		return err
	}
	return writer.DeleteConnection(ctx, c.ID.String())
}
