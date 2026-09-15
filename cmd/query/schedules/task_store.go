package schedules

import (
	"context"
	"fmt"

	"github.com/flanksource/clicky/task"
)

// TaskScheduleStore adapts the lazy database provider to Clicky's scheduling
// persistence without opening PostgreSQL while the command tree is built.
func (p StoreProvider) TaskScheduleStore() task.ScheduleStore {
	return providerScheduleStore{provider: p}
}

type providerScheduleStore struct {
	provider StoreProvider
}

func (s providerScheduleStore) resolve() (*Store, error) {
	if s.provider == nil {
		return nil, fmt.Errorf("schedule store provider is required")
	}
	return s.provider()
}

func (s providerScheduleStore) ListSchedules(ctx context.Context) ([]task.Schedule, error) {
	store, err := s.resolve()
	if err != nil {
		return nil, err
	}
	return store.ListSchedules(ctx)
}

func (s providerScheduleStore) SaveSchedule(ctx context.Context, schedule task.Schedule) error {
	store, err := s.resolve()
	if err != nil {
		return err
	}
	return store.SaveSchedule(ctx, schedule)
}

func (s providerScheduleStore) DeleteSchedule(ctx context.Context, name string) error {
	store, err := s.resolve()
	if err != nil {
		return err
	}
	return store.DeleteSchedule(ctx, name)
}

func (s providerScheduleStore) RecordFire(ctx context.Context, name string, fire task.Fire) error {
	store, err := s.resolve()
	if err != nil {
		return err
	}
	return store.RecordFire(ctx, name, fire)
}
