// A reader's writes: published to the spool, the owner nudged through its
// socket, and the outcome awaited in the ledger the owner commits it to.
package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/recordstore"
)

// submit is a reader's recordstore.Submitter: it publishes batch as the next
// batch of this process and waits for the owner to apply it.
func (s *Store[T]) submit(ctx context.Context, batch recordstore.Batch) (recordstore.BatchResult, error) {
	batch.Producer = s.producer.Next()
	if err := s.publish(batch); err != nil {
		return recordstore.BatchResult{}, err
	}
	return s.Await(ctx, batch.ID)
}

// publish writes batch into the spool and nudges the owner to ingest it.
func (s *Store[T]) publish(batch recordstore.Batch) error {
	name, err := s.spool.Publish(batch, s.options.Format)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.published[batch.ID] = name
	s.mu.Unlock()
	// The nudge only saves the owner's poll; a failed one changes nothing.
	_, _ = ControlIngest(s.socket, []string{batch.ID}, false, time.Second)
	return nil
}

// Await waits for the batch id to be applied and returns what it did. A
// batch of this process's that the owner could not read at all is an error;
// when ctx ends first the batch is still pending, and the error is a
// *PendingError.
func (s *Store[T]) Await(ctx context.Context, id string) (recordstore.BatchResult, error) {
	for {
		if ctx.Err() != nil {
			return recordstore.BatchResult{}, &PendingError{BatchID: id}
		}
		backend, err := s.Backend()
		if err != nil {
			return recordstore.BatchResult{}, err
		}
		result, found, err := backend.BatchOutcome(ctx, id)
		if err != nil && ctx.Err() == nil {
			return recordstore.BatchResult{}, err
		}
		if found {
			s.mu.Lock()
			delete(s.published, id)
			s.mu.Unlock()
			return result, nil
		}
		if err := s.failed(id); err != nil {
			return recordstore.BatchResult{}, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(awaitPoll):
		}
	}
}

// failed reports the batch id this process published as failed, with the
// reason the owner recorded, once the owner moved it to failed/.
func (s *Store[T]) failed(id string) error {
	s.mu.Lock()
	name, ok := s.published[id]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	encoded, err := os.ReadFile(filepath.Join(s.spool.Path(), "failed", name, "error.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("batch %s failed: %w", id, err)
	}
	var reason struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(encoded, &reason); err != nil {
		return fmt.Errorf("batch %s failed: %s", id, encoded)
	}
	return fmt.Errorf("batch %s failed: %s", id, reason.Error)
}
