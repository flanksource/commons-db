package profiles

import (
	"context"
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/query"
)

type VirtualStore interface {
	Store
	Peek(context.Context, string) (query.Profile, error)
	IsVirtual(string) bool
}

type peekStore interface {
	Peek(context.Context, string) (query.Profile, error)
}

type nonTouchStore struct{ Store }

func (s nonTouchStore) Get(ctx context.Context, name string) (query.Profile, error) {
	if store, ok := s.Store.(peekStore); ok {
		return store.Peek(ctx, name)
	}
	return s.Store.Get(ctx, name)
}

func ResolveWithoutTouch(ctx context.Context, store Store, name string) (ResolvedProfile, error) {
	return Resolve(ctx, nonTouchStore{Store: store}, name)
}

type OverlayStore struct {
	base    Store
	virtual VirtualStore
}

func NewOverlayStore(base Store, virtual VirtualStore) (*OverlayStore, error) {
	if base == nil || virtual == nil {
		return nil, fmt.Errorf("profile overlay requires base and virtual stores")
	}
	return &OverlayStore{base: base, virtual: virtual}, nil
}

func (s *OverlayStore) List(ctx context.Context) ([]query.Profile, error) {
	base, err := s.base.List(ctx)
	if err != nil {
		return nil, err
	}
	virtual, err := s.virtual.List(ctx)
	if err != nil {
		return nil, err
	}
	items := append(base, virtual...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *OverlayStore) Get(ctx context.Context, name string) (query.Profile, error) {
	if s.virtual.IsVirtual(name) {
		return s.virtual.Get(ctx, name)
	}
	return s.base.Get(ctx, name)
}

func (s *OverlayStore) Peek(ctx context.Context, name string) (query.Profile, error) {
	if s.virtual.IsVirtual(name) {
		return s.virtual.Peek(ctx, name)
	}
	if store, ok := s.base.(peekStore); ok {
		return store.Peek(ctx, name)
	}
	return s.base.Get(ctx, name)
}

func (s *OverlayStore) Save(ctx context.Context, profile query.Profile) error {
	if s.virtual.IsVirtual(profile.Name) {
		return fmt.Errorf("virtual profile %q is read-only", profile.Name)
	}
	return s.base.Save(ctx, profile)
}

func (s *OverlayStore) Update(ctx context.Context, name string, profile query.Profile, options UpdateOptions) error {
	if s.virtual.IsVirtual(name) || s.virtual.IsVirtual(profile.Name) {
		return fmt.Errorf("virtual profile %q is read-only", name)
	}
	return s.base.Update(ctx, name, profile, options)
}

func (s *OverlayStore) Delete(ctx context.Context, name string) error {
	if s.virtual.IsVirtual(name) {
		return fmt.Errorf("virtual profile %q is read-only", name)
	}
	return s.base.Delete(ctx, name)
}

func (s *OverlayStore) IsVirtual(name string) bool { return s.virtual.IsVirtual(name) }
