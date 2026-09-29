// Closing a store: an owner drains the spool and hands the store back; any
// process then ingests what was published after that, while nobody owns it.
package owner

import (
	"context"
	"errors"
)

// recheckRounds bounds how often Close takes a free store to drain batches
// published while it was letting go.
const recheckRounds = 3

// Close releases this process's hold. The owner stops serving, drains the
// spool for up to DrainOnClose, withdraws its state and unlocks; a reader
// stops waiting for the lock. Either then ingests any batch still in the
// spool if nobody else owns the store, so a producer that published while
// the owner let go is not left waiting for the next owner.
func (s *Store[T]) Close() error {
	if s.shared && !release(s.configured) {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.shutdown() })
	return s.closeErr
}

func (s *Store[T]) shutdown() error {
	s.cancel()
	s.running.Wait()
	s.mu.Lock()
	role, backend, opened, lock, control, ingest := s.role, s.backend, s.opened, s.lock, s.control, s.ingest
	s.mu.Unlock()
	var errs []error
	if role == RoleOwner {
		state := s.ownerState()
		state.Phase = PhaseDraining
		errs = append(errs, writeState(s.configured, state))
		if control != nil {
			errs = append(errs, control.Close())
		}
		drain, cancel := context.WithTimeout(context.Background(), s.options.DrainOnClose)
		_, err := ingest.drain(drain)
		cancel()
		failpoint("after-final-drain")
		if err != nil && drain.Err() == nil {
			errs = append(errs, err)
		}
		errs = append(errs, removeState(s.configured, s.instance), backend.Close(), lock.Close())
	} else if opened {
		errs = append(errs, backend.Close())
	}
	errs = append(errs, s.recheck())
	return errors.Join(errs...)
}

// recheck ingests what is left in the spool while the store is free.
func (s *Store[T]) recheck() error {
	for range recheckRounds {
		names, err := s.spool.Incoming()
		if err != nil || len(names) == 0 {
			return err
		}
		lock, held, err := tryLock(s.configured)
		if err != nil || !held {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.options.DrainOnClose)
		backend, err := s.options.Open(ctx, false, nil)
		if err != nil {
			cancel()
			return errors.Join(err, lock.Close())
		}
		_, err = newIngester(s.spool, backend, s.configured).drain(ctx)
		if ctx.Err() != nil {
			err = nil
		}
		cancel()
		if err := errors.Join(err, backend.Close(), lock.Close()); err != nil {
			return err
		}
	}
	return nil
}
